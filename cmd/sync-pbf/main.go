// Baca OSM PBF (mis. Geofabrik indonesia-latest.osm.pbf) pakai paulmach/osm,
// filter by bbox + kategori yg relevan, upsert ke tabel poi. Menghindari dep
// system (osmium-tool, Docker). 2-pass:
//   pass 1: scan NODES — upsert yg punya tag relevan; cache coords dari semua
//           node di dalam bbox (dipake pass 2).
//   pass 2: scan WAYS — kalo tag relevan, ambil centroid dari ref nodes di cache.
//
// Usage: PBF_PATH=data/indonesia-latest.osm.pbf go run ./cmd/sync-pbf
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/paulmach/osm"
	"github.com/paulmach/osm/osmpbf"
)

type bbox struct{ minLat, minLon, maxLat, maxLon float64 }

func (b bbox) contains(lat, lon float64) bool {
	return lat >= b.minLat && lat <= b.maxLat && lon >= b.minLon && lon <= b.maxLon
}

// Prioritas kategori — urutan menentukan key mana yg dipake sbg `category`
// kalo element punya >1 tag (mis. amenity + tourism).
var categoryKeys = []string{"tourism", "amenity", "shop", "leisure", "historic", "natural", "place"}

type poiRow struct {
	osmType     string
	osmID       int64
	name        string
	category    string
	subcategory string
	lat, lon    float64
	tags        map[string]string
}

func pickCategory(tags osm.Tags) (cat, sub string) {
	for _, k := range categoryKeys {
		if v := tags.Find(k); v != "" {
			return k, v
		}
	}
	return "", ""
}

// Semua tag non-meta dimasukin ke JSONB (buat ditampilin di popup).
func tagsToMap(tags osm.Tags) map[string]string {
	m := make(map[string]string, len(tags))
	for _, t := range tags {
		if strings.HasPrefix(t.Key, "@") {
			continue
		}
		m[t.Key] = t.Value
	}
	return m
}

func parseBBox(s string) (bbox, error) {
	var b bbox
	n, err := fmt.Sscanf(s, "%f,%f,%f,%f", &b.minLat, &b.minLon, &b.maxLat, &b.maxLon)
	if err != nil || n != 4 {
		return b, fmt.Errorf("JAKARTA_BBOX format salah (expect minLat,minLon,maxLat,maxLon): %v", err)
	}
	return b, nil
}

// Centroid buat way:
//   - Kalo closed polygon (first ref == last ref, >=4 pts), pake shoelace (proper
//     geometric centroid).
//   - Kalo open polyline atau degenerate (area ~0, mis. collinear), fallback ke
//     bbox center biar stabil.
// Input pts: [lat, lon].
func wayCentroid(pts [][2]float64) (lat, lon float64) {
	closed := len(pts) >= 4 && pts[0] == pts[len(pts)-1]
	if closed {
		var a, cx, cy float64
		for i := 0; i < len(pts)-1; i++ {
			x0, y0 := pts[i][1], pts[i][0]
			x1, y1 := pts[i+1][1], pts[i+1][0]
			cross := x0*y1 - x1*y0
			a += cross
			cx += (x0 + x1) * cross
			cy += (y0 + y1) * cross
		}
		a /= 2
		if a != 0 {
			return cy / (6 * a), cx / (6 * a)
		}
	}
	minLat, maxLat := pts[0][0], pts[0][0]
	minLon, maxLon := pts[0][1], pts[0][1]
	for _, p := range pts[1:] {
		if p[0] < minLat {
			minLat = p[0]
		}
		if p[0] > maxLat {
			maxLat = p[0]
		}
		if p[1] < minLon {
			minLon = p[1]
		}
		if p[1] > maxLon {
			maxLon = p[1]
		}
	}
	return (minLat + maxLat) / 2, (minLon + maxLon) / 2
}

// Pipe PBF file through osmpbf.Scanner dengan filter type callback.
func scanPBF(path string, fn func(osm.Object)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := osmpbf.New(context.Background(), f, 4) // 4 worker threads
	defer scanner.Close()
	for scanner.Scan() {
		fn(scanner.Object())
	}
	return scanner.Err()
}

func main() {
	_ = godotenv.Load()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL not set")
	}
	pbfPath := os.Getenv("PBF_PATH")
	if pbfPath == "" {
		pbfPath = "data/indonesia-latest.osm.pbf"
	}
	bboxStr := os.Getenv("JAKARTA_BBOX")
	if bboxStr == "" {
		log.Fatal("JAKARTA_BBOX not set")
	}
	bb, err := parseBBox(bboxStr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("bbox: lat[%.4f, %.4f] lon[%.4f, %.4f]", bb.minLat, bb.maxLat, bb.minLon, bb.maxLon)

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()

	// --- PASS 1: nodes ---
	// Dua tugas: (a) kumpulin coord SEMUA node di bbox utk resolve way centroid,
	// (b) langsung upsert node yg punya tag relevan.
	log.Println("pass 1: scanning nodes…")
	nodeCoords := make(map[int64][2]float64, 2_000_000)
	var nodePOIs []poiRow
	err = scanPBF(pbfPath, func(o osm.Object) {
		n, ok := o.(*osm.Node)
		if !ok {
			return
		}
		if !bb.contains(n.Lat, n.Lon) {
			return
		}
		nodeCoords[int64(n.ID)] = [2]float64{n.Lat, n.Lon}
		name := strings.TrimSpace(n.Tags.Find("name"))
		if name == "" {
			return
		}
		cat, sub := pickCategory(n.Tags)
		if cat == "" {
			return
		}
		nodePOIs = append(nodePOIs, poiRow{
			osmType: "node", osmID: int64(n.ID),
			name: name, category: cat, subcategory: sub,
			lat: n.Lat, lon: n.Lon, tags: tagsToMap(n.Tags),
		})
	})
	if err != nil {
		log.Fatalf("pass1: %v", err)
	}
	log.Printf("pass 1: %d node coords in bbox, %d candidate node POIs", len(nodeCoords), len(nodePOIs))

	// --- PASS 2: ways ---
	// Cuma way yg punya tag relevan + >=1 ref node dalam bbox yg di-upsert.
	// Centroid pake average coord sederhana dari ref nodes yg ada di cache.
	log.Println("pass 2: scanning ways…")
	var wayPOIs []poiRow
	err = scanPBF(pbfPath, func(o osm.Object) {
		w, ok := o.(*osm.Way)
		if !ok {
			return
		}
		name := strings.TrimSpace(w.Tags.Find("name"))
		if name == "" {
			return
		}
		cat, sub := pickCategory(w.Tags)
		if cat == "" {
			return
		}
		// Kumpulin coord ref nodes yg ada di cache (di dalam bbox).
		pts := make([][2]float64, 0, len(w.Nodes))
		for _, nr := range w.Nodes {
			if c, ok := nodeCoords[int64(nr.ID)]; ok {
				pts = append(pts, c)
			}
		}
		if len(pts) == 0 {
			return // semua node di luar bbox → skip
		}
		lat, lon := wayCentroid(pts)
		wayPOIs = append(wayPOIs, poiRow{
			osmType: "way", osmID: int64(w.ID),
			name: name, category: cat, subcategory: sub,
			lat: lat, lon: lon,
			tags: tagsToMap(w.Tags),
		})
	})
	if err != nil {
		log.Fatalf("pass2: %v", err)
	}
	log.Printf("pass 2: %d way POIs", len(wayPOIs))

	// --- UPSERT ---
	all := append(nodePOIs, wayPOIs...)
	log.Printf("upserting %d POIs…", len(all))

	var upserted, failed int
	for _, p := range all {
		tagsJSON, _ := json.Marshal(p.tags)
		_, err := pool.Exec(ctx, `
            INSERT INTO poi (osm_type, osm_id, name, category, subcategory, location, tags, updated_at)
            VALUES ($1, $2, $3, $4, $5,
                    ST_SetSRID(ST_MakePoint($6, $7), 4326)::geography,
                    $8, NOW())
            ON CONFLICT (osm_type, osm_id) DO UPDATE SET
                name        = EXCLUDED.name,
                category    = EXCLUDED.category,
                subcategory = EXCLUDED.subcategory,
                location    = EXCLUDED.location,
                tags        = EXCLUDED.tags,
                updated_at  = NOW()`,
			p.osmType, p.osmID, p.name, p.category, p.subcategory, p.lon, p.lat, tagsJSON)
		if err != nil {
			if failed < 5 {
				log.Printf("upsert %s/%d: %v", p.osmType, p.osmID, err)
			}
			failed++
			continue
		}
		upserted++
	}
	log.Printf("upserted=%d failed=%d", upserted, failed)

	var total int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM poi").Scan(&total); err != nil {
		log.Fatalf("count: %v", err)
	}
	log.Printf("poi table now has %d rows", total)
	if total == 0 {
		log.Fatal("sanity: tabel kosong setelah sync")
	}
}
