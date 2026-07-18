-- Idempotency for the hold path (api-contract.md): a client or load balancer
-- that retries POST /hold after a lost response must not create a duplicate
-- hold nor receive a false 409. The client supplies an Idempotency-Key header;
-- the first request records (key -> booking_id) atomically with the hold, and
-- any retry with the same key replays the original booking.
--
-- Confirm and cancel need no key table: they are already idempotent by their
-- CAS design (a second confirm/cancel affects zero rows and collapses to the
-- same terminal state), so this covers the one operation that genuinely needs
-- it.
CREATE TABLE idempotency_keys (
  key        text PRIMARY KEY,
  booking_id bigint NOT NULL REFERENCES bookings(id),
  created_at timestamptz NOT NULL DEFAULT now()
);

-- Keys accumulate; cmd/worker prunes rows older than the retry horizon
-- (24 hours — see PruneIdempotencyKeys) each poll tick, keeping this table
-- bounded and releasing the FK pins on old bookings rows. The index
-- supports that cleanup scan.
CREATE INDEX ix_idempotency_keys_created ON idempotency_keys (created_at);
