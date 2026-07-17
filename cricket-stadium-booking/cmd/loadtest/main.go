// cmd/loadtest is the Definition of Done for this build (spec.md): it
// drives the real HTTP API with concurrent goroutines, then asserts
// correctness from live Postgres state — not client-side counters.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stadiumbooking/internal/config"
	"stadiumbooking/internal/store"
)

const (
	baseURL          = "http://localhost:8080"
	concurrency      = 500 // ADR-001/PRD hard requirement: >=500 concurrent attempts
	contendedSeats   = 5   // small seat pool -> real contention, not just parallelism
	warmupIterations = 20
	expiryRacers     = 100
)

// httpClient is configured per spec.md's non-negotiable requirement:
// Go's default transport allows only 2 idle connections per host, which
// would silently serialize requests under this load and produce a
// FALSE-GREEN result (zero oversells because contention never actually
// happened, not because the constraint held). MaxIdleConnsPerHost must be
// >= concurrency.
var httpClient = &http.Client{
	Timeout: 5 * time.Second,
	Transport: &http.Transport{
		MaxIdleConnsPerHost: 600,
		MaxConnsPerHost:     0, // unbounded — let the pool, not the client, be the limiter under test
	},
}

type latencyTracker struct {
	mu    sync.Mutex
	times []time.Duration
}

func (l *latencyTracker) record(d time.Duration) {
	l.mu.Lock()
	l.times = append(l.times, d)
	l.mu.Unlock()
}

func (l *latencyTracker) p95() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.times) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), l.times...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(float64(len(sorted)) * 0.95)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

type scenarioResult struct {
	attempts, confirmed, rejected int64
	holdLatency, confirmLatency   *latencyTracker
}

func newScenarioResult() scenarioResult {
	return scenarioResult{holdLatency: &latencyTracker{}, confirmLatency: &latencyTracker{}}
}

func main() {
	ctx := context.Background()
	matchID := fmt.Sprintf("loadtest-%d", time.Now().UnixNano())

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	pool, err := store.NewPool(ctx, cfg)
	if err != nil {
		log.Fatalf("db connect (for invariant checks + fixture setup): %v", err)
	}
	defer pool.Close()
	db := store.New(pool)

	seedLoadTestFixtures(ctx, pool, matchID)

	fmt.Println("--- warm-up phase (discarded from stats) ---")
	warmup(matchID)

	fmt.Println("--- scenario A: pure contention ---")
	scenarioA := runContentionScenario(matchID)

	fmt.Println("--- scenario B: hold-expiry boundary race ---")
	scenarioB := runExpiryBoundaryScenario(ctx, pool, matchID)

	fmt.Println("--- verifying invariants from live Postgres state ---")
	oversold, err := db.OversoldSeats(ctx)
	if err != nil {
		log.Fatalf("invariant query failed: %v", err)
	}
	stuckHolds, err := db.StuckHolds(ctx)
	if err != nil {
		log.Fatalf("invariant query failed: %v", err)
	}

	report(scenarioA, scenarioB, oversold, stuckHolds)
}

// seedLoadTestFixtures creates a fresh, isolated match+seat set for this run
// (unique matchID per run) so results are never contaminated by prior
// manual testing or earlier runs. Idempotent via ON CONFLICT DO NOTHING.
func seedLoadTestFixtures(ctx context.Context, pool *pgxpool.Pool, matchID string) {
	_, err := pool.Exec(ctx, `
		INSERT INTO matches (id, name, start_time) VALUES ($1, 'Load Test Match', now() + interval '7 days')
		ON CONFLICT (id) DO NOTHING`, matchID)
	if err != nil {
		log.Fatalf("seed match: %v", err)
	}

	var seatIDs []string
	for i := 0; i < warmupIterations; i++ {
		seatIDs = append(seatIDs, fmt.Sprintf("WARMUP-%d", i))
	}
	for i := 0; i < contendedSeats; i++ {
		seatIDs = append(seatIDs, fmt.Sprintf("CONTEND-%d", i))
	}
	seatIDs = append(seatIDs, "EXPIRY-BOUNDARY")

	for _, seatID := range seatIDs {
		_, err := pool.Exec(ctx, `
			INSERT INTO seats (match_id, seat_id, section) VALUES ($1, $2, 'LOADTEST')
			ON CONFLICT (match_id, seat_id) DO NOTHING`, matchID, seatID)
		if err != nil {
			log.Fatalf("seed seat %s: %v", seatID, err)
		}
	}
}

func warmup(matchID string) {
	for i := 0; i < warmupIterations; i++ {
		seatID := fmt.Sprintf("WARMUP-%d", i)
		resp, err := postJSON(baseURL+"/matches/"+matchID+"/seats/"+seatID+"/hold", map[string]string{"buyer_id": "warmup"})
		if err == nil {
			resp.Body.Close()
		}
	}
}

func runContentionScenario(matchID string) scenarioResult {
	res := newScenarioResult()
	var wg sync.WaitGroup
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		go func(i int) {
			defer wg.Done()
			atomic.AddInt64(&res.attempts, 1)

			seatID := fmt.Sprintf("CONTEND-%d", rand.Intn(contendedSeats))
			buyerID := fmt.Sprintf("buyer-%d@example.com", i)

			start := time.Now()
			holdResp, err := postJSON(baseURL+"/matches/"+matchID+"/seats/"+seatID+"/hold", map[string]string{"buyer_id": buyerID})
			res.holdLatency.record(time.Since(start))
			if err != nil {
				atomic.AddInt64(&res.rejected, 1)
				return
			}
			defer holdResp.Body.Close()
			if holdResp.StatusCode != http.StatusCreated {
				atomic.AddInt64(&res.rejected, 1)
				return
			}

			var hold struct {
				HoldID string `json:"hold_id"`
			}
			if err := json.NewDecoder(holdResp.Body).Decode(&hold); err != nil {
				atomic.AddInt64(&res.rejected, 1)
				return
			}

			start = time.Now()
			confirmResp, err := postJSON(baseURL+"/holds/"+hold.HoldID+"/confirm", map[string]string{"buyer_id": buyerID})
			res.confirmLatency.record(time.Since(start))
			if err != nil {
				atomic.AddInt64(&res.rejected, 1)
				return
			}
			defer confirmResp.Body.Close()
			if confirmResp.StatusCode == http.StatusOK {
				atomic.AddInt64(&res.confirmed, 1)
			} else {
				atomic.AddInt64(&res.rejected, 1)
			}
		}(i)
	}
	wg.Wait()
	return res
}

// runExpiryBoundaryScenario places a hold, backdates its expiry directly in
// Postgres to simulate time passing (deterministic, no server-config
// juggling needed), then races concurrent re-holders against the
// now-expired seat and confirms the original hold correctly fails.
func runExpiryBoundaryScenario(ctx context.Context, pool *pgxpool.Pool, matchID string) scenarioResult {
	seatID := "EXPIRY-BOUNDARY"

	holdResp, err := postJSON(baseURL+"/matches/"+matchID+"/seats/"+seatID+"/hold", map[string]string{"buyer_id": "expiring-buyer@example.com"})
	if err != nil {
		log.Fatalf("scenario B: initial hold failed: %v", err)
	}
	var hold struct {
		HoldID string `json:"hold_id"`
	}
	if err := json.NewDecoder(holdResp.Body).Decode(&hold); err != nil {
		log.Fatalf("scenario B: decode hold response: %v", err)
	}
	holdResp.Body.Close()

	if _, err := pool.Exec(ctx, `
		UPDATE bookings SET hold_expires_at = now() - interval '1 second'
		WHERE match_id = $1 AND seat_id = $2 AND status = 'held'`, matchID, seatID); err != nil {
		log.Fatalf("scenario B: backdate expiry failed: %v", err)
	}

	res := newScenarioResult()
	var wg sync.WaitGroup
	wg.Add(expiryRacers)
	for i := 0; i < expiryRacers; i++ {
		go func(i int) {
			defer wg.Done()
			atomic.AddInt64(&res.attempts, 1)
			buyerID := fmt.Sprintf("racer-%d@example.com", i)
			resp, err := postJSON(baseURL+"/matches/"+matchID+"/seats/"+seatID+"/hold", map[string]string{"buyer_id": buyerID})
			if err != nil {
				atomic.AddInt64(&res.rejected, 1)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusCreated {
				atomic.AddInt64(&res.confirmed, 1) // "confirmed" here means "won the re-hold race"
			} else {
				atomic.AddInt64(&res.rejected, 1)
			}
		}(i)
	}
	wg.Wait()

	// The original hold must now fail to confirm — it's expired.
	confirmResp, err := postJSON(baseURL+"/holds/"+hold.HoldID+"/confirm", map[string]string{"buyer_id": "expiring-buyer@example.com"})
	if err == nil {
		defer confirmResp.Body.Close()
		if confirmResp.StatusCode != http.StatusConflict {
			log.Printf("WARNING: expired hold %s did not correctly reject confirm (got %d, want 409)", hold.HoldID, confirmResp.StatusCode)
		}
	}
	return res
}

func postJSON(url string, body any) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request body: %w", err)
	}
	return httpClient.Post(url, "application/json", strings.NewReader(string(b)))
}

func report(a, b scenarioResult, oversold, stuckHolds int) {
	fmt.Println()
	fmt.Println("=== LOAD TEST REPORT ===")
	fmt.Printf("Scenario A (contention, %d goroutines vs %d seats):\n", concurrency, contendedSeats)
	fmt.Printf("  attempts=%d confirmed=%d rejected=%d (sum check: %v)\n",
		a.attempts, a.confirmed, a.rejected, a.attempts == a.confirmed+a.rejected)
	fmt.Printf("  hold p95=%s confirm p95=%s (SLO: <200ms each)\n", a.holdLatency.p95(), a.confirmLatency.p95())
	fmt.Println()
	fmt.Printf("Scenario B (hold-expiry boundary, %d racers on 1 expired seat):\n", expiryRacers)
	fmt.Printf("  attempts=%d won-race=%d rejected=%d\n", b.attempts, b.confirmed, b.rejected)
	fmt.Println()
	fmt.Printf("Oversold seats (must be 0): %d\n", oversold)
	fmt.Printf("Stuck expired holds not yet reclaimable (informational): %d\n", stuckHolds)
	fmt.Println()

	pass := oversold == 0 && b.confirmed == 1
	if pass {
		fmt.Println("RESULT: PASS — zero oversells, expiry boundary correctly resolved to exactly one winner")
	} else {
		fmt.Println("RESULT: FAIL")
		os.Exit(1)
	}
}
