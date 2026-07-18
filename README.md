# Boundary — Cricket Stadium Booking

Boundary is a concurrency-safe cricket stadium seat-booking application. It combines an interactive oval stadium experience with a Go and PostgreSQL backend designed to answer the hardest question in high-demand ticketing:

> What happens when hundreds or thousands of people try to reserve the same small set of seats at the same time?

The application lets a buyer explore a visual cricket ground, select a numbered seat, place a temporary hold, confirm the booking, review previous bookings, cancel a confirmed seat, and track its refund. More importantly, it makes the database—not browser state or application timing—the final authority on whether a seat can be sold.

This repository is both a working application and a compact reference implementation of contention-safe booking patterns: compare-and-swap updates, database-enforced uniqueness, bounded retries, idempotency keys, short-lived holds, transactional outbox processing, conditional HTTP reads, request coalescing, and horizontally safe background workers.

## Table of contents

- [Why this application exists](#why-this-application-exists)
- [What a user can do](#what-a-user-can-do)
- [The most important guarantee](#the-most-important-guarantee)
- [System architecture](#system-architecture)
- [Booking lifecycle](#booking-lifecycle)
- [How concurrent booking is made safe](#how-concurrent-booking-is-made-safe)
- [How cancellation and refunds work](#how-cancellation-and-refunds-work)
- [How the application handles scale](#how-the-application-handles-scale)
- [Technology stack](#technology-stack)
- [Repository layout](#repository-layout)
- [Quick start](#quick-start)
- [Configuration](#configuration)
- [Using the application](#using-the-application)
- [API reference](#api-reference)
- [Database model and migrations](#database-model-and-migrations)
- [Testing and load testing](#testing-and-load-testing)
- [Observability and operations](#observability-and-operations)
- [Failure behavior](#failure-behavior)
- [Current scope and production boundaries](#current-scope-and-production-boundaries)
- [Design principles](#design-principles)

## Why this application exists

Seat booking appears simple until demand becomes concentrated. Reading a seat as “available” and later writing “confirmed” is unsafe because many requests can read the same state before any of them writes. Application-level mutexes are also insufficient once more than one server instance exists.

Boundary was built to demonstrate a safer model:

1. A seat is temporarily held before it can be confirmed.
2. PostgreSQL enforces that only one active hold or confirmation can exist for a seat.
3. Confirmation succeeds only while the caller owns a live hold.
4. A buyer can hold only one seat per match at a time, while still being allowed to confirm multiple seats sequentially.
5. Cancellation records the booking change and refund request atomically.
6. Slow external work is handled outside the request path by a background worker.
7. Read traffic is collapsed and compressed so polling clients do not translate directly into equal database load.

The seeded demo represents the India vs Australia final at Boundary National Stadium. It contains 400 seats across the North, South, East, and West stands.

## What a user can do

The browser application supports the complete demonstration flow:

- Explore an oval cricket stadium with curved, tiered seating.
- See individual seat numbers and availability states.
- Select a seat and receive immediate visual feedback.
- Hold the selected seat for a configurable period.
- Replace an existing hold atomically by choosing another seat.
- Confirm a held seat.
- See owned confirmed seats directly on the stadium map.
- Review confirmed and cancelled bookings in the **My bookings** tab.
- Select an owned confirmed seat on the map to begin cancellation.
- Cancel before the match starts.
- See a refund move from pending to refunded.
- Receive seat-map changes made by other clients through a ten-second refresh cycle.

The UI uses a buyer ID as a demonstration identity. It is not authentication; see [Current scope and production boundaries](#current-scope-and-production-boundaries).

## The most important guarantee

**A seat cannot have more than one active booking.**

That invariant is enforced in PostgreSQL by a partial unique index:

```sql
CREATE UNIQUE INDEX ux_bookings_active_seat
  ON bookings (match_id, seat_id)
  WHERE status IN ('held', 'confirmed');
```

Even if multiple API processes, goroutines, browser tabs, or retries reach the system simultaneously, PostgreSQL permits only one active row for a `(match_id, seat_id)` pair. Losing requests fail with a conflict instead of overselling the seat.

This matters because the database is the one component shared by every application instance. The invariant therefore remains valid after horizontal scaling; it does not depend on an in-memory lock in a particular Go process.

## System architecture

```mermaid
flowchart LR
    Browser["Browser application<br/>HTML, CSS, vanilla JavaScript"]
    API["Go HTTP API<br/>validation, deadlines, retries"]
    Cache["Per-instance seat cache<br/>1-second TTL + singleflight"]
    DB[("PostgreSQL<br/>source of truth")]
    Worker["Refund worker<br/>bounded polling batches"]
    Gateway["Payment gateway adapter<br/>stubbed in this build"]

    Browser -->|"holds, confirms, cancellations"| API
    Browser -->|"conditional seat reads every 10s"| API
    API --> Cache
    Cache --> DB
    API -->|"transactional writes"| DB
    Worker -->|"claim outbox events"| DB
    Worker --> Gateway
    Worker -->|"record refund result"| DB
```

### Runtime components

| Component | Responsibility |
|---|---|
| Browser UI | Stadium rendering, seat selection, hold countdown, booking history, cancellation confirmation, and refund-status display |
| API server | Serves the embedded frontend and exposes booking HTTP endpoints on port `8080` |
| Booking service | Applies request deadlines, bounded retries, cache invalidation, and domain-to-storage translation |
| PostgreSQL store | Owns transactions, compare-and-swap updates, uniqueness constraints, booking state, refunds, and the outbox |
| Refund worker | Claims outbox events, performs the refund adapter call, records completion, sweeps expired holds, and prunes old idempotency keys |
| Load-test command | Generates real HTTP contention and verifies invariants directly from PostgreSQL |

The frontend is embedded into the Go server binary with `go:embed`. There is no separate Node.js build, frontend development server, or runtime dependency for serving the application.

## Booking lifecycle

```mermaid
stateDiagram-v2
    state "Cancelled booking" as BookingCancelled
    [*] --> Available
    Available --> Held: Place hold
    Held --> Confirmed: Confirm before expiry
    Held --> Available: TTL passes
    Held --> Available: Release or replace hold
    Confirmed --> BookingCancelled: Cancel before match starts
    BookingCancelled --> RefundPending: Refund row + outbox event
    RefundPending --> Refunded: Worker completes refund
```

“Available” is derived rather than stored as a booking status. A seat is available when it has no active, unexpired `held` row and no `confirmed` row.

### Hold

A hold creates a `bookings` row with status `held` and a database-clock expiry timestamp. The default lifetime is five minutes.

If the same buyer already holds another seat in that match, the old hold is cancelled and the new hold is inserted in one transaction. If the new seat is unavailable, the entire transaction rolls back and the old hold remains intact.

### Confirm

Confirmation is a single compare-and-swap SQL update. It succeeds only if all of the following remain true at update time:

- The hold ID exists.
- The supplied buyer owns it.
- Its status is still `held`.
- Its expiry is later than the PostgreSQL clock’s current time.

The update changes the row to `confirmed`, records `confirmed_at`, and clears the hold expiry.

### Release and expiry

A buyer can voluntarily release an active hold. Expired holds are also treated as available immediately by seat reads, even if the cleanup worker has not yet rewritten their stored status. The worker later sweeps old held rows in bounded batches to keep the hot table and its indexes clean.

### Cancel

A confirmed booking can be cancelled only by its buyer and only before the match start time. Cancellation frees the seat immediately and begins the asynchronous refund workflow.

## How concurrent booking is made safe

### 1. Database-enforced seat uniqueness

The partial unique index on active bookings is the final oversell prevention mechanism. Concurrent insert attempts do not rely on which request reached Go first. PostgreSQL selects one winner and returns unique-violation error `23505` to the rest; the API translates those losses into HTTP `409 Conflict`.

### 2. Compare-and-swap state transitions

Confirm, release, cancel, and refund completion use conditional `UPDATE` statements. State changes happen only when the row is still in the expected prior state. This prevents stale clients from repeating or reversing completed transitions.

For example, confirmation is guarded by `status = 'held' AND hold_expires_at > now()`. A late confirm cannot revive an expired hold.

### 3. One active hold per buyer per match

The database also contains a partial unique index on `(match_id, buyer_id)` for held rows. A transaction-scoped PostgreSQL advisory lock serializes replacement attempts from the same buyer, including requests arriving from different tabs or API instances.

Confirmed bookings are excluded from this constraint. A buyer may confirm several seats, but cannot hoard several unconfirmed seats simultaneously.

### 4. Atomic hold replacement

Replacing seat A with seat B is one transaction:

1. Lock the buyer-and-match replacement scope.
2. Cancel the buyer’s previous held seat, excluding the target seat.
3. Lazily expire a stale hold on the target seat.
4. Insert the new hold.
5. Commit everything together.

If step 4 loses a seat race, rollback restores seat A. The application never silently confirms the old seat, and it does not discard a valid old hold just because the desired replacement failed.

### 5. Optional idempotency for hold requests

Clients may send an `Idempotency-Key` header when placing a hold. The key and resulting booking ID are committed atomically.

- Repeating the same key with the same match, seat, and buyer replays the live result.
- Reusing the key for different parameters returns HTTP `422`.
- If the mapped hold is dead, the same logical attempt may proceed fresh.
- Keys older than 24 hours are pruned by the worker in bounded batches.

Confirm and cancel already use compare-and-swap semantics, so repeating them cannot create duplicate terminal transitions.

### 6. Bounded deadlines and retries

Every service operation receives a configurable request deadline. This stops goroutines from waiting indefinitely for a saturated connection pool or slow query.

Only PostgreSQL serialization failures (`40001`) and deadlocks (`40P01`) are retried. Retries are bounded by `MAX_RETRIES` and use full-jitter exponential backoff capped at one second. Backoff respects context cancellation, so the retry loop cannot outlive the request deadline.

### 7. Bounded database connections

The `pgxpool` connection pool has a hard maximum. Increasing API concurrency therefore does not create unbounded PostgreSQL connections. The default is 20 connections per process and should be sized against the database’s total connection budget before adding API or worker replicas.

## How cancellation and refunds work

Calling a payment provider inside the cancellation request would couple user latency and booking correctness to an external network dependency. Boundary instead uses the transactional outbox pattern.

The cancellation transaction performs three writes atomically:

1. Changes the confirmed booking to `cancelled`.
2. Creates a `refunds` row with status `pending`.
3. Creates a `refund_requested` outbox event.

Either all three commit or none of them do. The API can return after the durable intent exists, without waiting for the gateway.

The worker polls unprocessed events every two seconds. It claims a bounded batch with `FOR UPDATE SKIP LOCKED`, which allows multiple worker processes to divide work without claiming the same row. A 30-second claim lease lets another worker reclaim an event if its original worker crashes.

After the refund adapter succeeds, the worker marks the refund `refunded` and records an external reference. The update is idempotent. If the database update fails transiently, the worker releases the claim so the event can be retried on the next poll rather than waiting for the lease to expire.

Unknown event types and invalid payloads are logged as dead-letter conditions and marked processed so a permanently bad event cannot block newer events.

> **Important:** the current refund adapter is deliberately stubbed. It generates a unique simulated gateway reference and never moves real money.

## How the application handles scale

Correctness and throughput are treated separately. PostgreSQL constraints protect correctness; the following mechanisms reduce avoidable work.

### Browser and HTTP efficiency

- Visible pages refresh the seat map every 10 seconds instead of every two seconds.
- Refresh scheduling is completion-based, so a slow request cannot create overlapping polls.
- Polling pauses while the document is hidden and resumes when it becomes visible.
- Seat responses carry weak ETags. Unchanged snapshots return a header-only `304 Not Modified`.
- HTML and JavaScript are precompressed once at process startup.
- Dynamic JSON uses pooled, low-CPU gzip writers.
- Static responses use cache validators and explicit cache policies.
- The absent favicon returns an empty cached `204` rather than the full HTML document.
- The frontend reconciles only changed seat and booking elements instead of rebuilding all 400 seats.
- One delegated seat listener replaces hundreds of per-seat closures.
- Stadium layout is CSS-contained to reduce browser layout and paint scope.

### Read-load collapse

Seat maps are cached per match for one second inside each API process. Concurrent misses for the same match are collapsed through Go’s `singleflight`, so a burst of clients results in one database query for that process and cache window rather than one query per client.

Every successful hold, confirm, release, or cancel invalidates the affected match’s local cache, preserving immediate read-your-writes behavior on that instance. The database remains authoritative; cache staleness can affect display freshness but cannot create an oversell.

### Write-path behavior under contention

- Seat conflicts fail quickly at the unique index instead of waiting on a large application lock.
- Transactions are short and limited to the state that must change atomically.
- The application retries only errors that can safely benefit from a retry.
- Request deadlines and pool bounds provide overload backpressure.
- The load-test client opens enough connections to create real contention rather than accidentally serializing traffic in the HTTP transport.

### Worker throughput and maintenance

- Outbox events are processed in configurable bounded batches.
- `SKIP LOCKED` permits horizontal worker scaling.
- Expired holds are swept in batches of 1,000.
- Idempotency keys older than 24 hours are pruned in batches of 1,000.
- Partial indexes keep active-seat enforcement, expiry scans, and outbox polling focused on hot rows.
- The bookings table uses a lower autovacuum scale factor because hold/expire/re-hold traffic creates high row churn.

### Horizontal-scaling characteristics

API instances can share PostgreSQL without weakening the no-oversell guarantee. Worker instances can also scale horizontally because claims use row locking plus leases.

Two optimizations are intentionally process-local:

- The one-second seat cache is not distributed. Different API instances can display snapshots that differ for up to the cache window, although all writes remain safe.
- The optional IP rate limiter is in memory. Limits are per instance and reset on restart.

A larger deployment would normally place a CDN or gateway before the API, move abuse controls to a shared edge layer, and use database notifications, a shared cache, or push updates when sub-second cross-instance display freshness is required.

## Technology stack

| Layer | Technology | Why it is used |
|---|---|---|
| Backend | Go 1.25.5, standard `net/http` | Small runtime, straightforward concurrency, graceful shutdown, and explicit middleware |
| Database | PostgreSQL | Transactions, partial unique indexes, advisory locks, compare-and-swap SQL, `SKIP LOCKED`, and database-clock expiry |
| PostgreSQL driver | `pgx/v5` and `pgxpool` | Native PostgreSQL support and bounded connection pooling |
| Request coalescing | `golang.org/x/sync/singleflight` | Collapses concurrent seat-cache misses |
| Rate limiting | `golang.org/x/time/rate` | Optional per-IP token bucket for mutating requests |
| Frontend | Semantic HTML, CSS, vanilla JavaScript | No framework or build pipeline; minimal client payload and direct control of rendering |
| Static delivery | Go `embed`, ETags, gzip | A self-contained server binary with efficient browser revalidation |
| Tests | Go `testing` and `httptest` | Unit, store, HTTP contract, concurrency, caching, and delivery tests |

## Repository layout

After cloning, the Go application lives in the inner `cricket-stadium-booking` directory:

```text
.
├── README.md
└── cricket-stadium-booking/
    ├── cmd/
    │   ├── server/       # HTTP API and embedded frontend server
    │   ├── worker/       # Refund outbox and maintenance worker
    │   └── loadtest/     # Concurrent HTTP load and invariant verification
    ├── internal/
    │   ├── booking/      # Domain service, deadlines, retries, seat cache
    │   ├── config/       # Environment parsing and validation
    │   ├── httpapi/      # Routes, handlers, middleware, compression, caching
    │   ├── observability/# Structured transition logging
    │   └── store/        # PostgreSQL transactions and queries
    ├── migrations/       # Ordered schema and index migrations
    ├── web/
    │   ├── index.html    # Stadium UI and styles
    │   ├── app.js        # Browser state and API interactions
    │   └── embed.go      # Embeds frontend assets into the server binary
    ├── go.mod
    └── go.sum
```

## Quick start

### Prerequisites

- Go `1.25.5` or a compatible toolchain capable of honoring the module’s toolchain requirement.
- A running PostgreSQL server.
- PostgreSQL command-line tools such as `createdb` and `psql`.

### 1. Clone and enter the application directory

```bash
git clone https://github.com/Aaratb/cricket-stadium-booking.git
cd cricket-stadium-booking/cricket-stadium-booking
```

### 2. Create the database

```bash
createdb cricket_stadium_booking
```

The default connection URL is:

```text
postgres:///cricket_stadium_booking?host=/tmp
```

That default is convenient for a local Unix-socket PostgreSQL installation. If your database uses TCP, a different socket path, credentials, or a container, set `DATABASE_URL`, for example:

```bash
export DATABASE_URL='postgres://postgres:postgres@localhost:5432/cricket_stadium_booking?sslmode=disable'
```

### 3. Apply every migration in order

From the inner application directory:

```bash
for migration in migrations/*.sql; do
  psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f "$migration"
done
```

If you are using the default local database and have not exported `DATABASE_URL`, run:

```bash
for migration in migrations/*.sql; do
  psql cricket_stadium_booking -v ON_ERROR_STOP=1 -f "$migration"
done
```

Migration `0002_seed_fixtures.sql` creates match `m1` and 400 seats across four sections.

### 4. Start the API server

```bash
go run ./cmd/server
```

The server connects to PostgreSQL at startup, serves the API and frontend, listens on `:8080`, and shuts down gracefully on `SIGINT` or `SIGTERM`.

### 5. Start the worker in a second terminal

Use the same `DATABASE_URL` as the server:

```bash
go run ./cmd/worker
```

The booking flow works without the worker, but refunds will remain pending and cleanup will not run until the worker is active.

### 6. Open the application

Visit [http://localhost:8080](http://localhost:8080).

Verify database connectivity with:

```bash
curl http://localhost:8080/healthz
```

Expected response:

```json
{"status":"ok"}
```

## Configuration

All settings are environment variables. Invalid safety-critical values fail startup validation.

| Variable | Default | Meaning |
|---|---:|---|
| `DATABASE_URL` | `postgres:///cricket_stadium_booking?host=/tmp` | PostgreSQL connection string |
| `HOLD_TTL` | `5m` | How long a seat remains held; must be greater than zero and no more than one hour |
| `POOL_MAX_CONNS` | `20` | Maximum PostgreSQL connections per process; must be greater than zero |
| `REQUEST_TIMEOUT` | `2s` | Hard service-operation deadline; must be greater than zero |
| `OUTBOX_BATCH_SIZE` | `100` | Maximum refund events claimed by a worker poll; must be greater than zero |
| `MAX_RETRIES` | `3` | Additional retry bound for serialization failures and deadlocks; cannot be negative |
| `RATE_LIMIT_ENABLED` | `false` | Enables the in-memory per-IP limiter for mutating requests |

When enabled, the limiter allows 10 mutating requests per second per source IP with a burst of 20. GET requests are not limited. Idle IP entries are removed after ten minutes.

The limiter is disabled by default because the included load test is intentionally a high-volume single-IP client. Enable it for an exposed single-instance demo, but use a shared gateway-level control for a real multi-instance deployment.

## Using the application

1. Enter a buyer ID. An email-like value is convenient, but the current implementation requires only a non-empty string of at most 255 bytes.
2. Select a green available seat. The blue outline is immediate client feedback; it does not yet reserve the seat.
3. Choose **Hold selected seat**. The server arbitrates the hold and starts the visible countdown.
4. Choose **Confirm** before the countdown expires, or release the hold.
5. Confirmed seats owned by the current buyer are selectable from the stadium map for cancellation.
6. Use **My bookings** to review confirmed and cancelled seats and open the same cancellation action.
7. After cancellation, watch the refund status. With the worker running it should change from pending to refunded after processing.

Changing the buyer ID refreshes booking ownership. A red confirmed seat belonging to another buyer remains unavailable and cannot be cancelled by the current buyer.

## API reference

All JSON write bodies are capped at 64 KiB. JSON responses use `Cache-Control: no-store` unless an endpoint explicitly defines a safe read policy.

### Endpoint summary

| Method | Path | Purpose | Typical success |
|---|---|---|---:|
| `GET` | `/` | Embedded browser application | `200` |
| `GET` | `/app.js` | Embedded frontend JavaScript | `200` or `304` |
| `GET` | `/healthz` | PostgreSQL connectivity check | `200` |
| `GET` | `/matches/{matchId}/seats` | Current derived seat map | `200` or `304` |
| `POST` | `/matches/{matchId}/seats/{seatId}/hold` | Hold or atomically replace a held seat | `201` |
| `POST` | `/holds/{holdId}/confirm` | Confirm a live owned hold | `200` |
| `DELETE` | `/holds/{holdId}` | Release an owned hold | `204` |
| `GET` | `/matches/{matchId}/bookings?buyer_id=...` | Buyer booking and refund history | `200` |
| `POST` | `/bookings/{bookingId}/cancel` | Cancel a confirmed booking before match start | `200` |

### List seats

```bash
curl -i http://localhost:8080/matches/m1/seats
```

Seat status is one of `available`, `held`, or `confirmed`. Held seats include an expiry timestamp.

The response includes an ETag. Send it back with `If-None-Match`; an unchanged map returns `304` without a JSON body.

### Place a hold

```bash
curl -i \
  -X POST http://localhost:8080/matches/m1/seats/NORTH-1/hold \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: demo-hold-north-1-attempt-1' \
  -d '{"buyer_id":"alice@example.com"}'
```

Example response shape:

```json
{
  "hold_id": "123",
  "match_id": "m1",
  "seat_id": "NORTH-1",
  "buyer_id": "alice@example.com",
  "status": "held",
  "hold_expires_at": "2026-07-18T18:30:00Z"
}
```

IDs are encoded as JSON strings to preserve 64-bit integer precision in JavaScript clients.

### Confirm a hold

```bash
curl -i \
  -X POST http://localhost:8080/holds/123/confirm \
  -H 'Content-Type: application/json' \
  -d '{"buyer_id":"alice@example.com"}'
```

### Release a hold

```bash
curl -i \
  -X DELETE http://localhost:8080/holds/123 \
  -H 'Content-Type: application/json' \
  -d '{"buyer_id":"alice@example.com"}'
```

### List a buyer’s bookings

```bash
curl -i 'http://localhost:8080/matches/m1/bookings?buyer_id=alice%40example.com'
```

The list contains confirmed bookings and previously confirmed bookings that were cancelled. Abandoned or replaced holds are intentionally excluded. Cancelled confirmed bookings include `refund_status` such as `pending` or `refunded`.

### Cancel a confirmed booking

```bash
curl -i \
  -X POST http://localhost:8080/bookings/123/cancel \
  -H 'Content-Type: application/json' \
  -d '{"buyer_id":"alice@example.com"}'
```

The cancellation response confirms the durable booking transition. Refund completion is asynchronous and appears through the booking-list endpoint.

### Error responses

| Status | Error | Meaning |
|---:|---|---|
| `400` | `buyer_id required` or `invalid id` | Request validation failed |
| `409` | `seat_unavailable` | Another active hold or confirmation owns the seat |
| `409` | `hold_expired` | Hold or booking is no longer in the required state |
| `404` | `not_found` | Releasable hold was not found |
| `422` | `idempotency_key_reuse` | The key was previously used with different request parameters |
| `429` | `rate_limited` | Optional mutation rate limit was exceeded |
| `500` | `internal_error` | An unexpected server or database error occurred |
| `503` | `{"status":"down"}` | Health check could not reach PostgreSQL |

## Database model and migrations

Migrations are intentionally plain SQL and must be applied in filename order.

| Migration | Purpose |
|---|---|
| `0001_schema.sql` | Core matches, seats, bookings, active-seat uniqueness, indexes, and autovacuum tuning |
| `0002_seed_fixtures.sql` | Demo match `m1` and 400 stadium seats |
| `0003_refunds_outbox.sql` | Refund state and transactional outbox |
| `0004_seats_section_index.sql` | Covers match/section/seat ordering for seat-map reads |
| `0005_outbox_claim_lease.sql` | Adds worker claim leases for safe horizontal processing |
| `0006_idempotency_keys.sql` | Durable hold-request idempotency mappings and cleanup index |
| `0007_bookings_held_expiry_index.sql` | Accelerates bounded expired-hold sweeps |
| `0008_single_active_buyer_hold.sql` | Enforces one live hold per buyer per match |

### Main tables

- `matches`: match identity, display name, and start time.
- `seats`: stable seat inventory keyed by match and seat ID.
- `bookings`: held, confirmed, expired, and cancelled transitions.
- `refunds`: pending, refunded, or failed refund state.
- `outbox_events`: durable asynchronous work created in booking transactions.
- `idempotency_keys`: client retry keys mapped to the booking they created.

PostgreSQL’s clock is used for hold expiry and match-start checks. This avoids correctness depending on whether several API hosts have perfectly synchronized clocks.

## Testing and load testing

### Automated tests

The test suite covers configuration validation, service retries, cache behavior, hold contention, confirmation, release, cancellation, booking history, refunds, idempotency, outbox leases, maintenance jobs, HTTP compression, ETags, static assets, and invariant queries.

Tests use PostgreSQL, so create and migrate the test database first and ensure `DATABASE_URL` points to it.

```bash
go test -count=1 ./...
```

Using a disposable database is recommended because tests create isolated fixture matches and booking rows.

### Built-in contention test

Start the API server with rate limiting disabled, then run:

```bash
RATE_LIMIT_ENABLED=false go run ./cmd/loadtest
```

The load test creates a unique match for every run and executes:

- A warm-up phase excluded from measurements.
- 500 concurrent goroutines competing for five seats.
- 100 racers competing for one deliberately expired seat.
- Direct database invariant checks after the HTTP traffic.

It reports hold and confirmation p95 latency, confirmed and rejected totals, oversold-seat count, and expired-hold state. It exits successfully only when the database reports zero oversold seats and exactly one winner at the expiry boundary.

The printed latency SLO is under 200 ms for hold and confirm in the local harness. Treat that as a development target, not a universal production capacity claim: hardware, PostgreSQL configuration, network distance, data volume, and replica count all change the result.

## Observability and operations

### Health

`GET /healthz` performs a live PostgreSQL ping. It returns `200` with `{"status":"ok"}` when the dependency is reachable and `503` otherwise.

### Logs

Hold and confirmation transitions emit structured JSON to standard output with:

- Operation name.
- Match ID.
- Seat ID.
- Duration in milliseconds.
- Error text on failure.

Buyer IDs are deliberately excluded from transition logs to avoid placing personally identifiable information in an unredacted logging stream. The server and worker also emit lifecycle, cleanup, retry, and dead-letter messages.

### Graceful shutdown

The API listens for `SIGINT` and `SIGTERM`, stops accepting new traffic, and allows up to ten seconds for in-flight requests to finish. The worker stops on the same signals after its current operation returns.

### Useful database checks

Confirm that no seat has been sold more than once:

```sql
SELECT match_id, seat_id, count(*)
FROM bookings
WHERE status = 'confirmed'
GROUP BY match_id, seat_id
HAVING count(*) > 1;
```

Inspect the refund backlog:

```sql
SELECT count(*)
FROM outbox_events
WHERE processed_at IS NULL;
```

Inspect pending refunds:

```sql
SELECT id, booking_id, requested_at
FROM refunds
WHERE status = 'pending'
ORDER BY requested_at;
```

## Failure behavior

- **Two buyers hold one seat:** the database accepts one active row and the loser receives `409 seat_unavailable`.
- **A confirm arrives after expiry:** the guarded update changes no rows and the caller receives `409 hold_expired`.
- **A new-seat replacement fails:** the transaction rolls back, preserving the buyer’s previous hold.
- **The same hold request is retried after a lost response:** with the same idempotency key, the original live result is replayed.
- **PostgreSQL is saturated:** the bounded pool queues callers until their request deadlines expire instead of opening unlimited connections.
- **A serialization failure or deadlock occurs:** the service retries a bounded number of times with jitter while respecting the deadline.
- **A client disconnects during a shared seat-cache fill:** other waiters keep using the detached, independently bounded database load.
- **A worker crashes after claiming an event:** its lease expires after 30 seconds and another worker may reclaim it.
- **Refund-state persistence fails transiently:** the worker releases the claim and retries on the next poll.
- **A malformed permanent outbox event appears:** it is logged as a dead-letter condition and marked processed to avoid head-of-line blocking.
- **The worker is down:** cancellations remain durable with pending refunds; processing resumes when a worker returns.
- **An API process crashes:** committed booking state remains in PostgreSQL; uncommitted transactions roll back.

## Current scope and production boundaries

This repository demonstrates booking correctness and scalable application patterns, but it is not a complete production ticketing platform.

Before exposing it to real customers, address at least the following:

- **Authentication and authorization:** `buyer_id` is supplied by the client and is not proof of identity. A production API must derive ownership from an authenticated principal.
- **Real payments:** capture is not implemented and refunds are stubbed. Use a provider adapter with provider-side idempotency, signed webhooks, reconciliation, and secure secret management.
- **Distributed abuse protection:** the optional limiter is per process and IP-based. Put rate limits, bot mitigation, queueing, and admission control at a shared edge or gateway.
- **Transport security:** terminate TLS and configure trusted proxy handling before using forwarded client addresses.
- **Migration management:** use a deployment migration tool and release process rather than manually applying SQL in production.
- **Multi-match frontend:** the browser currently targets seeded match `m1`; production routing should select matches dynamically.
- **Inventory administration:** seat and match creation currently comes from SQL fixtures, not an organizer interface.
- **Notification delivery:** no email, SMS, ticket wallet, barcode, or entry-scanning system is included.
- **Refund failure UX:** the schema supports `failed`, but the demo worker always simulates success and the UI is optimized for pending/refunded states.
- **Shared caching or push updates:** the local cache is intentionally simple. Add distributed invalidation or event-driven updates when display freshness across instances must be tighter than one second.
- **Production telemetry:** connect logs to metrics, traces, dashboards, alerting, and SLO reporting.
- **Data lifecycle:** define archival, privacy retention, backup, restore, and disaster-recovery procedures.
- **Regional resilience:** decide how booking writes are routed to one authoritative PostgreSQL region or partitioned without weakening seat uniqueness.

These boundaries do not weaken the core no-oversell demonstration; they identify the surrounding product and operational systems required for real-money use.

## Design principles

The codebase follows a few deliberate rules:

1. **Put global correctness in the database.** Process-local synchronization cannot protect a horizontally scaled fleet.
2. **Make state transitions conditional.** A stale caller must not overwrite a newer state.
3. **Keep transactions short.** Do only the work that must commit atomically.
4. **Move external side effects out of request transactions.** Record durable intent, then process it asynchronously.
5. **Bound every resource.** Connections, retries, request time, worker batches, request bodies, cache age, leases, and retained idempotency keys all have limits.
6. **Treat retries as a normal distributed-systems event.** Make retryable operations idempotent and reject unsafe key reuse.
7. **Separate correctness from freshness.** A slightly stale seat display is acceptable; an oversold seat is not.
8. **Verify invariants from the source of truth.** The load test checks PostgreSQL directly rather than trusting client counters.
9. **Be honest about demo boundaries.** Stubbed payments and unauthenticated buyer IDs are stated explicitly rather than presented as production-ready features.

If you are new to the repository, start with the migrations to understand the invariants, then read `internal/store` for the atomic SQL transitions, `internal/booking` for deadlines and retries, `internal/httpapi` for the contract, and finally `web` for the user experience.
