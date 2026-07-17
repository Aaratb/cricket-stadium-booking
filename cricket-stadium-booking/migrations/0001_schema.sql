-- ADR-001: seats + bookings, partial unique index as the sole correctness guarantee.

CREATE TABLE seats (
  match_id  text NOT NULL,
  seat_id   text NOT NULL,
  section   text NOT NULL,
  PRIMARY KEY (match_id, seat_id)
);

CREATE TABLE matches (
  id         text PRIMARY KEY,
  name       text NOT NULL,
  start_time timestamptz NOT NULL
);

CREATE TABLE bookings (
  id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  match_id        text NOT NULL,
  seat_id         text NOT NULL,
  buyer_id        text NOT NULL,
  status          text NOT NULL CHECK (status IN ('held','confirmed','expired','cancelled')),
  hold_expires_at timestamptz,
  confirmed_at    timestamptz,
  cancelled_at    timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (match_id, seat_id) REFERENCES seats (match_id, seat_id) ON DELETE RESTRICT,
  FOREIGN KEY (match_id) REFERENCES matches (id) ON DELETE RESTRICT,
  CHECK (status <> 'held' OR hold_expires_at IS NOT NULL)
);

CREATE INDEX ix_bookings_seat ON bookings (match_id, seat_id);
CREATE INDEX ix_bookings_buyer ON bookings (buyer_id);

-- THE correctness guarantee: exactly one active (held or confirmed) row per seat.
CREATE UNIQUE INDEX ux_bookings_active_seat
  ON bookings (match_id, seat_id)
  WHERE status IN ('held', 'confirmed');

-- Tuned for high hold->expire->re-hold churn; default 0.2 scale factor bloats this table fast.
ALTER TABLE bookings SET (autovacuum_vacuum_scale_factor = 0.02, autovacuum_vacuum_cost_delay = 0);
