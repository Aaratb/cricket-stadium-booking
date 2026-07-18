-- One buyer may have at most one live hold per match. Confirmed bookings are
-- intentionally excluded: a buyer may book multiple seats, but cannot hoard
-- several seats concurrently while deciding which one to confirm.

-- Normalize rows that are logically expired but have not yet been swept so
-- they do not block creation of the partial unique index.
UPDATE bookings
SET status = 'expired'
WHERE status = 'held' AND hold_expires_at <= now();

-- Preserve the newest existing hold if older application versions allowed a
-- buyer to accumulate several holds in the same match.
WITH duplicate_holds AS (
  SELECT id,
         row_number() OVER (
           PARTITION BY match_id, buyer_id
           ORDER BY created_at DESC, id DESC
         ) AS position
  FROM bookings
  WHERE status = 'held'
)
UPDATE bookings b
SET status = 'cancelled', cancelled_at = now()
FROM duplicate_holds d
WHERE b.id = d.id AND d.position > 1;

CREATE UNIQUE INDEX ux_bookings_one_held_seat_per_buyer
  ON bookings (match_id, buyer_id)
  WHERE status = 'held';
