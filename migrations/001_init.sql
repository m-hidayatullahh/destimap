CREATE EXTENSION IF NOT EXISTS postgis;

CREATE TABLE IF NOT EXISTS poi (
    id          BIGSERIAL PRIMARY KEY,
    osm_type    TEXT NOT NULL,                       -- node/way/relation
    osm_id      BIGINT NOT NULL,
    name        TEXT NOT NULL,
    category    TEXT NOT NULL,                       -- tourism / amenity / shop / leisure / ...
    subcategory TEXT,                                -- museum / restaurant / mall / ...
    location    GEOGRAPHY(POINT, 4326) NOT NULL,
    tags        JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (osm_type, osm_id)
);

CREATE INDEX IF NOT EXISTS poi_location_gix ON poi USING GIST (location);
CREATE INDEX IF NOT EXISTS poi_category_idx ON poi (category);
CREATE INDEX IF NOT EXISTS poi_subcategory_idx ON poi (subcategory);
