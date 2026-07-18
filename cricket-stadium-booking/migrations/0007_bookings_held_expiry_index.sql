-- SweepExpiredHolds filters on status = 'held' AND hold_expires_at < now()
-- and orders by hold_expires_at, every poll tick, from every worker. Without
-- a matching index each sweep scans and sorts the hot bookings table even
-- when zero holds are expired. This partial index turns the idle sweep into
-- an empty index range scan, and stays tiny because it only covers rows that
-- are currently 'held'.
CREATE INDEX ix_bookings_held_expiry ON bookings (hold_expires_at) WHERE status = 'held';
