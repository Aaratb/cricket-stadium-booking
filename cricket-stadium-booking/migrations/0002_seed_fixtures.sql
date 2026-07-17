-- Organizer substitute: one match + a seat map sized for the load test.

INSERT INTO matches (id, name, start_time) VALUES
  ('m1', 'India vs Australia — Final', now() + interval '7 days');

-- 4 sections x 100 seats = 400 seats total.
INSERT INTO seats (match_id, seat_id, section)
SELECT 'm1', section || '-' || row_num, section
FROM (VALUES ('NORTH'), ('SOUTH'), ('EAST'), ('WEST')) AS s(section)
CROSS JOIN generate_series(1, 100) AS row_num;
