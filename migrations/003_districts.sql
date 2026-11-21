CREATE TABLE IF NOT EXISTS districts (
    id       SERIAL PRIMARY KEY,
    name     TEXT UNIQUE NOT NULL,              -- "Jakarta Barat", dll
    osm_id   BIGINT UNIQUE,
    boundary GEOGRAPHY(MULTIPOLYGON, 4326) NOT NULL
);
CREATE INDEX IF NOT EXISTS districts_boundary_gix ON districts USING GIST (boundary);
