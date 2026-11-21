-- Tambahin natural (pantai/puncak/air terjun/dll) + place (pulau) ke 'alam'.
-- GENERATED column ga bisa di-ALTER, jadi DROP + ADD lagi.
DROP INDEX IF EXISTS poi_tour_category_idx;
ALTER TABLE poi DROP COLUMN IF EXISTS tour_category;

ALTER TABLE poi ADD COLUMN tour_category TEXT
    GENERATED ALWAYS AS (
        CASE
            WHEN tags->>'amenity' = 'place_of_worship' THEN 'religi'
            WHEN tags->>'tourism' = 'museum' THEN 'sejarah'
            WHEN tags->>'historic' IN ('memorial','monument','castle','ruins','archaeological_site','battlefield','fort','manor','palace','city_gate','wayside_shrine') THEN 'sejarah'
            WHEN tags->>'historic' IN ('building','tower','church') THEN 'arsitektur'
            WHEN tags->>'amenity' IN ('restaurant','cafe','fast_food','food_court','bar','pub','ice_cream','biergarten') THEN 'kuliner'
            WHEN tags->>'amenity' IN ('conference_centre','events_venue','exhibition_centre') THEN 'mice'
            WHEN tags->>'tourism' IN ('gallery','artwork','theme_park') OR tags->>'amenity' IN ('theatre','arts_centre','cinema','community_centre') THEN 'budaya'
            WHEN tags->>'tourism' IN ('hotel','hostel','guest_house','motel','apartment','chalet','camp_site','caravan_site') THEN 'akomodasi'
            WHEN tags->>'leisure' IN ('pitch','sports_centre','stadium','swimming_pool','fitness_centre','track','golf_course','horse_riding') THEN 'olahraga'
            WHEN tags->>'leisure' IN ('nature_reserve','garden')
                 OR tags->>'tourism' = 'viewpoint'
                 OR tags->>'natural' IN ('beach','peak','waterfall','cliff','bay','spring','cave_entrance','hot_spring','volcano','reef','wetland')
                 OR tags->>'place' IN ('island','islet')
                 THEN 'alam'
            WHEN tags ? 'shop' OR tags->>'amenity' = 'marketplace' THEN 'belanja'
            WHEN tags->>'tourism' IN ('attraction','zoo','aquarium','picnic_site') OR tags->>'leisure' IN ('park','playground','water_park') THEN 'rekreasi'
        END
    ) STORED;

CREATE INDEX IF NOT EXISTS poi_tour_category_idx ON poi (tour_category);
