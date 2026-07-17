-- ADR-002: cancellation/refund state + transactional outbox pattern.

CREATE TABLE refunds (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  booking_id   bigint NOT NULL REFERENCES bookings(id),
  status       text NOT NULL CHECK (status IN ('pending','refunded','failed')),
  requested_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  external_ref text
);

CREATE TABLE outbox_events (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  event_type   text NOT NULL,
  booking_id   bigint NOT NULL REFERENCES bookings(id),
  payload      jsonb NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  processed_at timestamptz
);

CREATE INDEX ix_outbox_unprocessed ON outbox_events (created_at) WHERE processed_at IS NULL;
