package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
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
	limit := clampInt(parseInt(q.Get("limit"), 50), 1, 200)
	offset := clampInt(parseInt(q.Get("offset"), 0), 0, 100000)

	rows, err := db.Query(r.Context(), `
        SELECT id, name, category, COALESCE(subcategory,''), COALESCE(tour_category,''),
               ST_Y(location::geometry) AS lat, ST_X(location::geometry) AS lon,
               tags
        FROM poi
        WHERE ($1 = '' OR category = $1)
          AND ($2 = '' OR subcategory = $2)
          AND ($3 = '' OR tour_category = $3)
        ORDER BY id
        LIMIT $4 OFFSET $5`,
		category, subcategory, tourType, limit, offset)
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
        ORDER BY meters
        LIMIT $7`,
		lon, lat, radius, category, subcategory, tourType, limit)
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
