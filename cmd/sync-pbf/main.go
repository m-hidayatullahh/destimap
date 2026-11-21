// Baca GeoJSON FeatureCollection dari stdin (keluaran osmium export),
// upsert ke tabel poi. Geometry non-point pakai ST_Centroid di PostGIS.
//
// Usage: ./scripts/sync-pbf.sh | go run ./cmd/sync-pbf
package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

type feature struct {
	Type       string          `json:"type"`
	Geometry   json.RawMessage `json:"geometry"`
	Properties map[string]any  `json:"properties"`
}

type featureCollection struct {
	Type     string    `json:"type"`
	Features []feature `json:"features"`
}

// Prioritas kategori — urutan menentukan key mana yg dipake sbg `category`
// kalo feature punya >1 tag (mis. amenity + tourism).
var categoryKeys = []string{"tourism", "amenity", "shop", "leisure", "historic", "natural", "place"}

func propString(props map[string]any, key string) string {
	if v, ok := props[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func propInt64(props map[string]any, key string) (int64, bool) {
	v, ok := props[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	}
	return 0, false
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

	log.Println("reading GeoJSON dari stdin…")
	var fc featureCollection
	dec := json.NewDecoder(os.Stdin)
	dec.UseNumber()
	if err := dec.Decode(&fc); err != nil {
		log.Fatalf("decode geojson: %v", err)
	}
	log.Printf("received %d features", len(fc.Features))

	var upserted, skipped int
	for _, f := range fc.Features {
		name := strings.TrimSpace(propString(f.Properties, "name"))
		if name == "" {
			skipped++
			continue
		}
		osmType := propString(f.Properties, "@type")
		osmID, ok := propInt64(f.Properties, "@id")
		if osmType == "" || !ok {
			skipped++
			continue
		}

		var cat, sub string
		for _, k := range categoryKeys {
			if s := propString(f.Properties, k); s != "" {
				cat, sub = k, s
				break
			}
		}
		if cat == "" {
			skipped++
			continue
		}

		// Semua properti selain @-prefix masuk tags (JSONB).
		tags := map[string]string{}
		for k, v := range f.Properties {
			if strings.HasPrefix(k, "@") {
				continue
			}
			if s, ok := v.(string); ok {
				tags[k] = s
			}
		}
		tagsJSON, _ := json.Marshal(tags)

		_, err := pool.Exec(ctx, `
            INSERT INTO poi (osm_type, osm_id, name, category, subcategory, location, tags, updated_at)
            VALUES ($1, $2, $3, $4, $5,
                    ST_Centroid(ST_GeomFromGeoJSON($6))::geography,
                    $7, NOW())
            ON CONFLICT (osm_type, osm_id) DO UPDATE SET
                name        = EXCLUDED.name,
                category    = EXCLUDED.category,
                subcategory = EXCLUDED.subcategory,
                location    = EXCLUDED.location,
                tags        = EXCLUDED.tags,
                updated_at  = NOW()`,
			osmType, osmID, name, cat, sub, string(f.Geometry), tagsJSON)
		if err != nil {
			log.Printf("upsert %s/%d: %v", osmType, osmID, err)
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
		log.Fatal("sanity check: tabel kosong setelah sync")
	}
}
