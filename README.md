# destimap

API service untuk data destinasi wisata Indonesia — menggabungkan OpenStreetMap (base data) dan Google Places (enrichment opsional), siap dikonsumsi mobile/web app.

## Status

MVP — fokus awal: Jakarta, kategori `tourism` (hotel, museum, attraction, guest_house, dll).

## Prasyarat

- Go 1.23+
- PostgreSQL 16+ dengan extension PostGIS 3+

## Setup

```bash
# 1. Siapkan database
createdb destimap
psql -d destimap -f migrations/001_init.sql

# 2. Konfigurasi
cp .env.example .env
# edit DATABASE_URL sesuai lokal lo

# 3. Pull data OSM Jakarta
go run ./cmd/sync

# 4. Nyalain API
go run ./cmd/api
```

## Endpoint

| Method | Path                      | Keterangan                                      |
|--------|---------------------------|-------------------------------------------------|
| GET    | `/healthz`                | Health check                                    |
| GET    | `/destinations`           | List. Query: `category`, `subcategory`, `limit`, `offset` |
| GET    | `/destinations/{id}`      | Detail per destinasi                            |
| GET    | `/nearby`                 | Query: `lat`, `lon`, `radius` (meter), `limit`, `category` |

Contoh:

```bash
curl "http://localhost:8080/nearby?lat=-6.1754&lon=106.8272&radius=500"
```

## Lisensi data

Data base dari © OpenStreetMap contributors, [ODbL](https://opendatacommons.org/licenses/odbl/).
