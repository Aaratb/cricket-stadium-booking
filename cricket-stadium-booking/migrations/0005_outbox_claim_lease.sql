-- Scale fix (real-scale-topology.md, horizontal-scaling): a single outbox
-- worker draining ORDER BY created_at LIMIT n is a throughput ceiling and a
-- single point of failure. To run N workers concurrently without any two
-- grabbing the same event, PollUnprocessed now claims rows with
-- SELECT ... FOR UPDATE SKIP LOCKED and stamps a lease (claimed_at).
--
-- The lease preserves the at-least-once guarantee: if a worker crashes after
-- claiming but before marking an event processed, the lease expires and
-- another worker reclaims it. processed_at (set only after the side effect
-- succeeds) remains the durable "done" marker; claimed_at is only an
-- in-flight lease.
ALTER TABLE outbox_events ADD COLUMN claimed_at timestamptz;

-- The existing ix_outbox_unprocessed (created_at) WHERE processed_at IS NULL
-- already serves the claim subquery's ORDER BY / filter, so no new index is
-- needed here.
