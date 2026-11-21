#!/bin/bash
# Pipeline sync dari OSM PBF extract (Geofabrik). Output: GeoJSON ke stdout.
# Usage:
#   ./scripts/sync-pbf.sh [path/to/indonesia-latest.osm.pbf] | go run ./cmd/sync-pbf
set -euo pipefail

PBF="${1:-data/indonesia-latest.osm.pbf}"

if [ ! -f "$PBF" ]; then
    echo "File not found: $PBF" >&2
    echo "Download dulu:" >&2
    echo "  curl -L https://download.geofabrik.de/asia/indonesia-latest.osm.pbf -o $PBF" >&2
    exit 1
fi

# bbox osmium: minLon,minLat,maxLon,maxLat (urutan beda dari Overpass)
# Jakarta + Kepulauan Seribu
BBOX="106.350,-6.370,106.975,-5.100"

WORK=$(mktemp -d)
trap "rm -rf $WORK" EXIT

echo "extracting Jakarta bbox…" >&2
osmium extract -b "$BBOX" "$PBF" -o "$WORK/jakarta.osm.pbf" --overwrite

echo "filtering tags…" >&2
osmium tags-filter "$WORK/jakarta.osm.pbf" \
    nwr/tourism \
    nwr/amenity \
    nwr/shop \
    nwr/leisure \
    nwr/historic \
    nw/natural=beach,peak,waterfall,cliff,bay,spring,cave_entrance,hot_spring,volcano,reef,wetland \
    nwr/place=island,islet \
    -o "$WORK/poi.osm.pbf" --overwrite

echo "exporting to GeoJSON…" >&2
osmium export "$WORK/poi.osm.pbf" -f geojson --overwrite -o -
