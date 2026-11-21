// Seeder utk tabel districts: 6 Kota/Kabupaten Administrasi DKI Jakarta.
// Ambil polygon dari Nominatim (public, gratis). Jalan sekali aja.
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

type nominatimFeature struct {
	Type       string                 `json:"type"`
	Properties map[string]any         `json:"properties"`
	Geometry   json.RawMessage        `json:"geometry"`
	BBox       []float64              `json:"bbox"`
	Extra      map[string]interface{} `json:"-"`
}

type nominatimFC struct {
	Features []nominatimFeature `json:"features"`
}

type target struct {
	Query string // Nominatim search query
	Label string // Nama yang disimpan di DB
}

var targets = []target{
	{"Kota Administrasi Jakarta Pusat", "Jakarta Pusat"},
	{"Kota Administrasi Jakarta Barat", "Jakarta Barat"},
	{"Kota Administrasi Jakarta Timur", "Jakarta Timur"},
	{"Kota Administrasi Jakarta Selatan", "Jakarta Selatan"},
	{"Kota Administrasi Jakarta Utara", "Jakarta Utara"},
	{"Kabupaten Administrasi Kepulauan Seribu", "Kepulauan Seribu"},
}

func main() {
	_ = godotenv.Load()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()

	client := &http.Client{Timeout: 30 * time.Second}

	for _, t := range targets {
		log.Printf("fetching %q…", t.Query)
		feat, err := fetchDistrict(ctx, client, t.Query)
		if err != nil {
			log.Printf("  skip %q: %v", t.Query, err)
			continue
		}
		osmID, _ := feat.Properties["osm_id"].(float64)

		_, err = pool.Exec(ctx, `
            INSERT INTO districts (name, osm_id, boundary)
            VALUES ($1, $2, ST_Multi(ST_GeomFromGeoJSON($3))::geography)
            ON CONFLICT (name) DO UPDATE SET
                osm_id   = EXCLUDED.osm_id,
                boundary = EXCLUDED.boundary`,
			t.Label, int64(osmID), string(feat.Geometry))
		if err != nil {
			log.Printf("  insert %s: %v", t.Label, err)
			continue
		}
		log.Printf("  -> %s osm_id=%d", t.Label, int64(osmID))

		time.Sleep(1100 * time.Millisecond) // Nominatim politeness
	}

	var cnt int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM districts").Scan(&cnt); err != nil {
		log.Fatal(err)
	}
	log.Printf("districts in DB: %d", cnt)
	if cnt == 0 {
		log.Fatal("sanity: no districts seeded")
	}
}

func fetchDistrict(ctx context.Context, client *http.Client, query string) (*nominatimFeature, error) {
	u := "https://nominatim.openstreetmap.org/search?" + url.Values{
		"q":               {query},
		"format":          {"geojson"},
		"polygon_geojson": {"1"},
		"limit":           {"1"},
		"countrycodes":    {"id"},
	}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	// Nominatim wajib User-Agent (ToS). Default Go client ditolak.
	req.Header.Set("User-Agent", "destimap-seeder/0.1 (contact: hidayatullahblk@gmail.com)")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var fc nominatimFC
	if err := json.NewDecoder(resp.Body).Decode(&fc); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if len(fc.Features) == 0 {
		return nil, fmt.Errorf("no features")
	}
	return &fc.Features[0], nil
}
