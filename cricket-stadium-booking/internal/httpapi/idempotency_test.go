package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func doRequestWithKey(t *testing.T, handler http.Handler, method, path, body, idempotencyKey string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Idempotency-Key", idempotencyKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestHandleHold_IdempotentReplay(t *testing.T) {
	handler, matchID := testServer(t)
	path := "/matches/" + matchID + "/seats/A1/hold"
	key := matchID + "-key"

	first := doRequestWithKey(t, handler, "POST", path, `{"buyer_id":"alice@example.com"}`, key)
	if first.Code != http.StatusCreated {
		t.Fatalf("first hold status = %d, body: %s", first.Code, first.Body.String())
	}
	retry := doRequestWithKey(t, handler, "POST", path, `{"buyer_id":"alice@example.com"}`, key)
	if retry.Code != http.StatusCreated {
		t.Fatalf("retry status = %d, want 201 (replay, not conflict), body: %s", retry.Code, retry.Body.String())
	}

	var a, b struct {
		HoldID string `json:"hold_id"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &a); err != nil {
		t.Fatalf("decode first: %v", err)
	}
	if err := json.Unmarshal(retry.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode retry: %v", err)
	}
	if a.HoldID != b.HoldID {
		t.Errorf("retry hold_id = %s, want original %s", b.HoldID, a.HoldID)
	}

	// Without a key, the same request is a genuine conflict.
	conflict := doRequest(t, handler, "POST", path, `{"buyer_id":"alice@example.com"}`)
	if conflict.Code != http.StatusConflict {
		t.Errorf("keyless duplicate status = %d, want 409", conflict.Code)
	}
}

func TestHandleHold_IdempotencyKeyReuseRejected(t *testing.T) {
	handler, matchID := testServer(t)
	path := "/matches/" + matchID + "/seats/A1/hold"
	key := matchID + "-key"

	first := doRequestWithKey(t, handler, "POST", path, `{"buyer_id":"alice@example.com"}`, key)
	if first.Code != http.StatusCreated {
		t.Fatalf("first hold status = %d, body: %s", first.Code, first.Body.String())
	}

	// Same key, different buyer: must be a 422 client error, never a replay
	// of alice's booking to bob.
	reuse := doRequestWithKey(t, handler, "POST", path, `{"buyer_id":"bob@example.com"}`, key)
	if reuse.Code != http.StatusUnprocessableEntity {
		t.Fatalf("reuse status = %d, want 422, body: %s", reuse.Code, reuse.Body.String())
	}
	if !strings.Contains(reuse.Body.String(), "idempotency_key_reuse") {
		t.Errorf("reuse body = %s, want idempotency_key_reuse", reuse.Body.String())
	}
}

// TestListSeats_FreshAfterEveryMutation pins read-your-writes across all
// four mutating paths: each one must invalidate the cached seat map, so a
// GET issued immediately after the mutation (well inside seatCacheTTL)
// reflects it.
func TestListSeats_FreshAfterEveryMutation(t *testing.T) {
	handler, matchID := testServer(t)

	seatStatus := func() string {
		t.Helper()
		rec := doRequest(t, handler, "GET", "/matches/"+matchID+"/seats", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("list seats status = %d", rec.Code)
		}
		var resp struct {
			Seats []struct {
				SeatID string `json:"seat_id"`
				Status string `json:"status"`
			} `json:"seats"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode seats: %v", err)
		}
		if len(resp.Seats) != 1 {
			t.Fatalf("seats = %+v, want exactly A1", resp.Seats)
		}
		return resp.Seats[0].Status
	}
	holdSeat := func() string {
		t.Helper()
		rec := doRequest(t, handler, "POST", "/matches/"+matchID+"/seats/A1/hold", `{"buyer_id":"alice@example.com"}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("hold status = %d, body: %s", rec.Code, rec.Body.String())
		}
		var hold struct {
			HoldID string `json:"hold_id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &hold); err != nil {
			t.Fatalf("decode hold: %v", err)
		}
		return hold.HoldID
	}

	// Populate the cache, then hold: the very next read must see it.
	if got := seatStatus(); got != "available" {
		t.Fatalf("initial status = %q, want available", got)
	}
	holdID := holdSeat()
	if got := seatStatus(); got != "held" {
		t.Errorf("status after hold = %q, want held (hold must invalidate the cache)", got)
	}

	// Confirm: cache was just populated by the read above.
	if rec := doRequest(t, handler, "POST", "/holds/"+holdID+"/confirm", `{"buyer_id":"alice@example.com"}`); rec.Code != http.StatusOK {
		t.Fatalf("confirm status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if got := seatStatus(); got != "confirmed" {
		t.Errorf("status after confirm = %q, want confirmed (confirm must invalidate the cache)", got)
	}

	// Cancel the confirmed booking: seat frees up.
	if rec := doRequest(t, handler, "POST", "/bookings/"+holdID+"/cancel", `{"buyer_id":"alice@example.com"}`); rec.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if got := seatStatus(); got != "available" {
		t.Errorf("status after cancel = %q, want available (cancel must invalidate the cache)", got)
	}

	// Hold again, then release: seat frees up again.
	holdID = holdSeat()
	if got := seatStatus(); got != "held" {
		t.Fatalf("status after re-hold = %q, want held", got)
	}
	if rec := doRequest(t, handler, "DELETE", "/holds/"+holdID, `{"buyer_id":"alice@example.com"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("release status = %d, body: %s", rec.Code, rec.Body.String())
	}
	if got := seatStatus(); got != "available" {
		t.Errorf("status after release = %q, want available (release must invalidate the cache)", got)
	}
}
