-- data.sql
-- Seeds the "items" table with 10,000 deterministic rows.
--
-- generate_series(1, 10000) produces the integers 1 through 10000.
-- The price formula (series * 37) % 10000 + 100 gives a deterministic but
-- varied price between 100 and 10099 for each item.
-- ON CONFLICT (id) DO NOTHING ensures this is idempotent:
-- re-running the script will not fail or create duplicates.

INSERT INTO items (id, name, price, description)
SELECT
    series,
    'Item ' || series,
    (series * 37) % 10000 + 100,
    'Deterministic seed item ' || series || ' for cache saturation experiments.'
FROM generate_series(1, 10000) AS series
ON CONFLICT (id) DO NOTHING;
