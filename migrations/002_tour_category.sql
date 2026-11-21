-- Kategori wisata (Indonesia-friendly) diturunkan otomatis dari OSM tags.
-- Prioritas dari atas ke bawah — pertama yang cocok yang menang.
ALTER TABLE poi ADD COLUMN IF NOT EXISTS tour_category TEXT
    GENERATED ALWAYS AS (
        CASE
            WHEN tags->>'amenity' = 'place_of_worship' THEN 'religi'
            WHEN tags->>'tourism' = 'museum' THEN 'sejarah'
            WHEN tags->>'historic' IN ('memorial','monument','castle','ruins','archaeological_site','battlefield','fort','manor','palace','city_gate','wayside_shrine') THEN 'sejarah'
            WHEN tags->>'historic' IN ('building','tower','church') THEN 'arsitektur'
            WHEN tags->>'amenity' IN ('restaurant','cafe','fast_food','food_court','bar','pub','ice_cream','biergarten') THEN 'kuliner'
            WHEN tags->>'amenity' IN ('conference_centre','events_venue','exhibition_centre') THEN 'mice'
            WHEN tags->>'tourism' IN ('gallery','artwork','theme_park') OR tags->>'amenity' IN ('theatre','arts_centre','cinema','community_centre') THEN 'budaya'
            WHEN tags->>'leisure' IN ('pitch','sports_centre','stadium','swimming_pool','fitness_centre','track','golf_course','horse_riding') THEN 'olahraga'
            WHEN tags->>'leisure' IN ('nature_reserve','garden') OR tags->>'tourism' = 'viewpoint' THEN 'alam'
            WHEN tags ? 'shop' OR tags->>'amenity' = 'marketplace' THEN 'belanja'
            WHEN tags->>'tourism' IN ('attraction','zoo','aquarium','picnic_site','camp_site') OR tags->>'leisure' IN ('park','playground','water_park') THEN 'rekreasi'
        END
    ) STORED;

CREATE INDEX IF NOT EXISTS poi_tour_category_idx ON poi (tour_category);
