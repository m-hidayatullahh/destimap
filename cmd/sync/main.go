package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

// shortcut: only tourism tag + public Overpass endpoint. upgrade when tourism-only
// validated end-to-end: add amenity/shop/leisure/historic, and move to a dedicated
// Overpass mirror if we hit rate limits.
const overpassEndpoint = "https://overpass-api.de/api/interpreter"

type overpassResp struct {
	Elements []overpassEl `json:"elements"`
}

type overpassEl struct {
	Type   string            `json:"type"`
	ID     int64             `json:"id"`
	Lat    *float64          `json:"lat,omitempty"`
	Lon    *float64          `json:"lon,omitempty"`
	Center *latLon           `json:"center,omitempty"`
	Tags   map[string]string `json:"tags"`
}

type latLon struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

func (e overpassEl) coord() (lat, lon float64, ok bool) {
	if e.Lat != nil && e.Lon != nil {
		return *e.Lat, *e.Lon, true
	}
	if e.Center != nil {
		return e.Center.Lat, e.Center.Lon, true
	}
	return 0, 0, false
}

var categoryKeys = []string{"tourism", "amenity", "shop", "leisure", "historic"}

func (e overpassEl) categorySubcategory() (string, string) {
	for _, k := range categoryKeys {
		if v, ok := e.Tags[k]; ok && v != "" {
			return k, v
		}
	}
	return "", ""
}

func main() {
	_ = godotenv.Load()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL not set")
	}
	bbox := os.Getenv("JAKARTA_BBOX")
	if bbox == "" {
		log.Fatal("JAKARTA_BBOX not set")
	}

	// Kategori yg di-pull — urutannya nggak ngaruh, kombinasi semua yg relevan utk destinasi
	query := fmt.Sprintf(`[out:json][timeout:180];
(
  node["tourism"](%[1]s);     way["tourism"](%[1]s);     relation["tourism"](%[1]s);
  node["amenity"](%[1]s);     way["amenity"](%[1]s);     relation["amenity"](%[1]s);
  node["shop"](%[1]s);        way["shop"](%[1]s);        relation["shop"](%[1]s);
  node["leisure"](%[1]s);     way["leisure"](%[1]s);     relation["leisure"](%[1]s);
  node["historic"](%[1]s);    way["historic"](%[1]s);    relation["historic"](%[1]s);
);
out center;`, bbox)

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("db connect: %v", err)
	}
	defer pool.Close()

	log.Println("querying Overpass…")
	client := &http.Client{Timeout: 300 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, overpassEndpoint,
		strings.NewReader(url.Values{"data": []string{query}}.Encode()))
	if err != nil {
		log.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "destimap-sync/0.1 (contact: hidayatullahblk@gmail.com)")
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("overpass request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		log.Fatalf("overpass %d: %s", resp.StatusCode, b)
	}

	var r overpassResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		log.Fatalf("decode: %v", err)
	}
	log.Printf("received %d elements", len(r.Elements))

	var upserted, skipped int
	for _, e := range r.Elements {
		name := strings.TrimSpace(e.Tags["name"])
		if name == "" {
			skipped++
			continue
		}
		lat, lon, ok := e.coord()
		if !ok {
			skipped++
			continue
		}
		cat, sub := e.categorySubcategory()
		if cat == "" {
			skipped++
			continue
		}
		tagsJSON, _ := json.Marshal(e.Tags)

		_, err := pool.Exec(ctx, `
            INSERT INTO poi (osm_type, osm_id, name, category, subcategory, location, tags, updated_at)
            VALUES ($1, $2, $3, $4, $5, ST_SetSRID(ST_MakePoint($6, $7), 4326)::geography, $8, NOW())
            ON CONFLICT (osm_type, osm_id) DO UPDATE SET
                name        = EXCLUDED.name,
                category    = EXCLUDED.category,
                subcategory = EXCLUDED.subcategory,
                location    = EXCLUDED.location,
                tags        = EXCLUDED.tags,
                updated_at  = NOW()`,
			e.Type, e.ID, name, cat, sub, lon, lat, tagsJSON)
		if err != nil {
			log.Printf("upsert %s/%d: %v", e.Type, e.ID, err)
			skipped++
			continue
		}
		upserted++
	}
	log.Printf("upserted=%d skipped=%d", upserted, skipped)

	var total int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM poi").Scan(&total); err != nil {
		log.Fatalf("count: %v", err)
	}
	log.Printf("poi table now has %d rows", total)
	if total == 0 {
		log.Fatal("sanity check failed: table is empty after sync")
	}
}
