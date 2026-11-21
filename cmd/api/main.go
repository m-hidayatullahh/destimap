package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

type poi struct {
	ID           int64             `json:"id"`
	Name         string            `json:"name"`
	Category     string            `json:"category"`
	Subcategory  string            `json:"subcategory,omitempty"`
	TourCategory string            `json:"tour_category,omitempty"`
	Lat          float64           `json:"lat"`
	Lon          float64           `json:"lon"`
	Tags         map[string]string `json:"tags,omitempty"`
	DistanceM    *float64          `json:"distance_m,omitempty"`
}

var db *pgxpool.Pool

func main() {
	_ = godotenv.Load()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL not set")
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	ctx := context.Background()
	var err error
	db, err = pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("db connect: %v", err)
	}
	defer db.Close()
	if err := db.Ping(ctx); err != nil {
		log.Fatalf("db ping: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealth)
	mux.HandleFunc("GET /destinations", handleList)
	mux.HandleFunc("GET /destinations/{id}", handleDetail)
	mux.HandleFunc("GET /nearby", handleNearby)
	mux.HandleFunc("GET /facilities", handleFacilities)
	mux.HandleFunc("GET /search", handleSearch)
	mux.HandleFunc("GET /districts", handleDistricts)
	mux.HandleFunc("GET /categories", handleCategories)
	mux.Handle("GET /", http.FileServer(http.Dir("web")))

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      logRequests(mux),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 15 * time.Second,
	}
	log.Printf("listening on :%s", port)
	log.Fatal(srv.ListenAndServe())
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.RequestURI(), time.Since(start))
	})
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := db.Ping(r.Context()); err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	category := q.Get("category")
	subcategory := q.Get("subcategory")
	tourType := q.Get("type")
	district := q.Get("district")
	limit := clampInt(parseInt(q.Get("limit"), 50), 1, 10000)
	offset := clampInt(parseInt(q.Get("offset"), 0), 0, 100000)

	rows, err := db.Query(r.Context(), `
        SELECT id, name, category, COALESCE(subcategory,''), COALESCE(tour_category,''),
               ST_Y(location::geometry) AS lat, ST_X(location::geometry) AS lon,
               tags
        FROM poi
        WHERE ($1 = '' OR category = $1)
          AND ($2 = '' OR subcategory = $2)
          AND ($3 = '' OR tour_category = $3)
          AND ($4 = '' OR ST_Intersects(location, (SELECT boundary FROM districts WHERE name = $4)))
        ORDER BY id
        LIMIT $5 OFFSET $6`,
		category, subcategory, tourType, district, limit, offset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()

	out := make([]poi, 0, limit)
	for rows.Next() {
		var p poi
		if err := rows.Scan(&p.ID, &p.Name, &p.Category, &p.Subcategory, &p.TourCategory, &p.Lat, &p.Lon, &p.Tags); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		out = append(out, p)
	}
	writeJSON(w, http.StatusOK, out)
}

func handleDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid id"))
		return
	}

	var p poi
	err = db.QueryRow(r.Context(), `
        SELECT id, name, category, COALESCE(subcategory,''), COALESCE(tour_category,''),
               ST_Y(location::geometry) AS lat, ST_X(location::geometry) AS lon,
               tags
        FROM poi WHERE id = $1`, id).
		Scan(&p.ID, &p.Name, &p.Category, &p.Subcategory, &p.TourCategory, &p.Lat, &p.Lon, &p.Tags)
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, http.StatusNotFound, errors.New("not found"))
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func handleNearby(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lat, err1 := strconv.ParseFloat(q.Get("lat"), 64)
	lon, err2 := strconv.ParseFloat(q.Get("lon"), 64)
	if err1 != nil || err2 != nil {
		writeErr(w, http.StatusBadRequest, errors.New("lat & lon required (float)"))
		return
	}
	radius := clampInt(parseInt(q.Get("radius"), 1000), 1, 50000) // meters
	limit := clampInt(parseInt(q.Get("limit"), 20), 1, 200)
	category := q.Get("category")
	subcategory := q.Get("subcategory")
	tourType := q.Get("type")
	district := q.Get("district")

	rows, err := db.Query(r.Context(), `
        WITH origin AS (
            SELECT ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography AS g
        )
        SELECT p.id, p.name, p.category, COALESCE(p.subcategory,''), COALESCE(p.tour_category,''),
               ST_Y(p.location::geometry), ST_X(p.location::geometry),
               p.tags,
               ST_Distance(p.location, origin.g) AS meters
        FROM poi p, origin
        WHERE ST_DWithin(p.location, origin.g, $3)
          AND ($4 = '' OR p.category = $4)
          AND ($5 = '' OR p.subcategory = $5)
          AND ($6 = '' OR p.tour_category = $6)
          AND ($7 = '' OR ST_Intersects(p.location, (SELECT boundary FROM districts WHERE name = $7)))
        ORDER BY meters
        LIMIT $8`,
		lon, lat, radius, category, subcategory, tourType, district, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()

	out := make([]poi, 0, limit)
	for rows.Next() {
		var p poi
		var meters float64
		if err := rows.Scan(&p.ID, &p.Name, &p.Category, &p.Subcategory, &p.TourCategory, &p.Lat, &p.Lon, &p.Tags, &meters); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		p.DistanceM = &meters
		out = append(out, p)
	}
	writeJSON(w, http.StatusOK, out)
}

type facility struct {
	Name      string  `json:"name"`
	Religion  string  `json:"religion,omitempty"` // relevan buat place_of_worship
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
	DistanceM float64 `json:"distance_m"`
}

// /facilities?lat=&lon=&radius=500 — ambil semua amenity/shop dalam radius,
// dikelompokin by subcategory (apapun yg OSM punya di area itu, ga hardcoded).
// Frontend bebas render apa aja sesuai keberadaan data.
func handleFacilities(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lat, err1 := strconv.ParseFloat(q.Get("lat"), 64)
	lon, err2 := strconv.ParseFloat(q.Get("lon"), 64)
	if err1 != nil || err2 != nil {
		writeErr(w, http.StatusBadRequest, errors.New("lat & lon required (float)"))
		return
	}
	radius := clampInt(parseInt(q.Get("radius"), 500), 50, 5000)
	perType := clampInt(parseInt(q.Get("per_type"), 3), 1, 10)

	// Kategori yg dianggap "fasilitas pendukung" — amenity & shop. Destinasi
	// tujuan (tourism/historic/leisure/natural/place) ga dimasukin ke sini.
	rows, err := db.Query(r.Context(), `
        WITH candidates AS (
            SELECT subcategory, name, tags->>'religion' AS religion,
                   ST_Y(location::geometry) AS lat, ST_X(location::geometry) AS lon,
                   ST_Distance(location, ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography) AS m,
                   ROW_NUMBER() OVER (
                       PARTITION BY subcategory
                       ORDER BY ST_Distance(location, ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography)
                   ) AS rn
            FROM poi
            WHERE category IN ('amenity','shop')
              AND subcategory <> ''
              AND ST_DWithin(location, ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography, $3)
        )
        SELECT subcategory, name, COALESCE(religion,''), lat, lon, m
        FROM candidates
        WHERE rn <= $4
        ORDER BY subcategory, m`, lon, lat, radius, perType)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()

	out := map[string][]facility{}
	for rows.Next() {
		var sub string
		var f facility
		if err := rows.Scan(&sub, &f.Name, &f.Religion, &f.Lat, &f.Lon, &f.DistanceM); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		out[sub] = append(out[sub], f)
	}
	writeJSON(w, http.StatusOK, out)
}

// Full-text style search sederhana pakai ILIKE ke name. Opsional lat/lon buat
// bias hasil berdasarkan jarak. Cocok buat typo-tolerant sih engga, tapi cukup
// buat demo — upgrade ke pg_trgm (tsvector/ts_rank) kalo udah ada > 100k row.
func handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	term := strings.TrimSpace(q.Get("q"))
	if term == "" {
		writeErr(w, http.StatusBadRequest, errors.New("q required"))
		return
	}
	limit := clampInt(parseInt(q.Get("limit"), 20), 1, 100)

	// Bias jarak kalo ada koordinat.
	lat, errLat := strconv.ParseFloat(q.Get("lat"), 64)
	lon, errLon := strconv.ParseFloat(q.Get("lon"), 64)
	hasOrigin := errLat == nil && errLon == nil

	like := "%" + term + "%"
	var rows pgx.Rows
	var err error
	if hasOrigin {
		rows, err = db.Query(r.Context(), `
            SELECT id, name, category, COALESCE(subcategory,''), COALESCE(tour_category,''),
                   ST_Y(location::geometry), ST_X(location::geometry),
                   tags,
                   ST_Distance(location, ST_SetSRID(ST_MakePoint($2, $3), 4326)::geography) AS m
            FROM poi
            WHERE name ILIKE $1
            ORDER BY m
            LIMIT $4`, like, lon, lat, limit)
	} else {
		rows, err = db.Query(r.Context(), `
            SELECT id, name, category, COALESCE(subcategory,''), COALESCE(tour_category,''),
                   ST_Y(location::geometry), ST_X(location::geometry),
                   tags,
                   NULL::double precision AS m
            FROM poi
            WHERE name ILIKE $1
            ORDER BY name
            LIMIT $2`, like, limit)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()

	out := make([]poi, 0, limit)
	for rows.Next() {
		var p poi
		var meters *float64
		if err := rows.Scan(&p.ID, &p.Name, &p.Category, &p.Subcategory, &p.TourCategory, &p.Lat, &p.Lon, &p.Tags, &meters); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		p.DistanceM = meters
		out = append(out, p)
	}
	writeJSON(w, http.StatusOK, out)
}

type categoryRow struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

var (
	catCacheMu  sync.Mutex
	catCacheAt  time.Time
	catCacheVal []categoryRow
)

const catCacheTTL = 5 * time.Minute

func handleCategories(w http.ResponseWriter, r *http.Request) {
	catCacheMu.Lock()
	if time.Since(catCacheAt) < catCacheTTL && catCacheVal != nil {
		out := catCacheVal
		catCacheMu.Unlock()
		writeJSON(w, http.StatusOK, out)
		return
	}
	catCacheMu.Unlock()

	rows, err := db.Query(r.Context(), `
        SELECT tour_category, COUNT(*) AS n
        FROM poi
        WHERE tour_category IS NOT NULL AND tour_category <> ''
        GROUP BY tour_category
        ORDER BY n DESC, tour_category`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	out := []categoryRow{}
	for rows.Next() {
		var c categoryRow
		if err := rows.Scan(&c.Name, &c.Count); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		out = append(out, c)
	}

	catCacheMu.Lock()
	catCacheVal = out
	catCacheAt = time.Now()
	catCacheMu.Unlock()

	writeJSON(w, http.StatusOK, out)
}

func handleDistricts(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query(r.Context(), `SELECT name FROM districts ORDER BY name`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		out = append(out, n)
	}
	writeJSON(w, http.StatusOK, out)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func parseInt(s string, dflt int) int {
	if s == "" {
		return dflt
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return dflt
	}
	return n
}

func clampInt(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}
