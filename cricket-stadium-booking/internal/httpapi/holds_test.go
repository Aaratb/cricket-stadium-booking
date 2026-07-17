package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stadiumbooking/internal/booking"
	"stadiumbooking/internal/store"
)

// testServer builds the full stack (handler -> service -> store) against
// the real local Postgres instance, per this project's established
// don't-mock-the-database approach -- an httptest handler test using a
// mocked service would only prove the JSON plumbing, not the actual
// contract this API makes.
func testServer(t *testing.T) (http.Handler, string) {
	t.Helper()
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, "postgres:///cricket_stadium_booking?host=/tmp")
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(pool.Close)

	matchID := fmt.Sprintf("httptest-%s-%d", t.Name(), time.Now().UnixNano())
	if _, err := pool.Exec(ctx, `INSERT INTO matches (id, name, start_time) VALUES ($1, 'test match', now() + interval '7 days')`, matchID); err != nil {
		t.Fatalf("seed match: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO seats (match_id, seat_id, section) VALUES ($1, 'A1', 'TEST')`, matchID); err != nil {
		t.Fatalf("seed seat: %v", err)
	}

	svc := booking.NewService(store.New(pool), 5*time.Minute, 2*time.Second, 3)
	return NewServer(svc, false), matchID // rate limiting off for tests, same as local dev default
}

func doRequest(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestHandleHold_SuccessAndConflict(t *testing.T) {
	handler, matchID := testServer(t)

	rec := doRequest(t, handler, "POST", "/matches/"+matchID+"/seats/A1/hold", `{"buyer_id":"alice@example.com"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first hold status = %d, want 201, body: %s", rec.Code, rec.Body.String())
	}

	rec2 := doRequest(t, handler, "POST", "/matches/"+matchID+"/seats/A1/hold", `{"buyer_id":"bob@example.com"}`)
	if rec2.Code != http.StatusConflict {
		t.Errorf("second hold status = %d, want 409, body: %s", rec2.Code, rec2.Body.String())
	}
}

func TestHandleHold_MissingBuyerID(t *testing.T) {
	handler, matchID := testServer(t)

	rec := doRequest(t, handler, "POST", "/matches/"+matchID+"/seats/A1/hold", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandleConfirm_FullFlow(t *testing.T) {
	handler, matchID := testServer(t)

	holdRec := doRequest(t, handler, "POST", "/matches/"+matchID+"/seats/A1/hold", `{"buyer_id":"alice@example.com"}`)
	if holdRec.Code != http.StatusCreated {
		t.Fatalf("hold status = %d, body: %s", holdRec.Code, holdRec.Body.String())
	}
	var hold struct {
		HoldID string `json:"hold_id"`
	}
	if err := json.Unmarshal(holdRec.Body.Bytes(), &hold); err != nil {
		t.Fatalf("decode hold response: %v", err)
	}

	confirmRec := doRequest(t, handler, "POST", "/holds/"+hold.HoldID+"/confirm", `{"buyer_id":"alice@example.com"}`)
	if confirmRec.Code != http.StatusOK {
		t.Fatalf("confirm status = %d, want 200, body: %s", confirmRec.Code, confirmRec.Body.String())
	}

	// GET /seats must now show A1 as confirmed -- the derived-status read
	// path, not just the write path.
	seatsRec := doRequest(t, handler, "GET", "/matches/"+matchID+"/seats", "")
	var seats struct {
		Seats []struct {
			SeatID string `json:"seat_id"`
			Status string `json:"status"`
		} `json:"seats"`
	}
	if err := json.Unmarshal(seatsRec.Body.Bytes(), &seats); err != nil {
		t.Fatalf("decode seats response: %v", err)
	}
	if len(seats.Seats) != 1 || seats.Seats[0].Status != "confirmed" {
		t.Errorf("seats = %+v, want exactly A1 confirmed", seats.Seats)
	}
}

func TestHandleConfirm_InvalidID(t *testing.T) {
	handler, _ := testServer(t)

	rec := doRequest(t, handler, "POST", "/holds/notanumber/confirm", `{"buyer_id":"alice@example.com"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandleCancel_FullFlow(t *testing.T) {
	handler, matchID := testServer(t)

	holdRec := doRequest(t, handler, "POST", "/matches/"+matchID+"/seats/A1/hold", `{"buyer_id":"alice@example.com"}`)
	var hold struct {
		HoldID string `json:"hold_id"`
	}
	json.Unmarshal(holdRec.Body.Bytes(), &hold)

	confirmRec := doRequest(t, handler, "POST", "/holds/"+hold.HoldID+"/confirm", `{"buyer_id":"alice@example.com"}`)
	var booking struct {
		BookingID string `json:"booking_id"`
	}
	json.Unmarshal(confirmRec.Body.Bytes(), &booking)

	cancelRec := doRequest(t, handler, "POST", "/bookings/"+booking.BookingID+"/cancel", `{"buyer_id":"alice@example.com"}`)
	if cancelRec.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, want 200, body: %s", cancelRec.Code, cancelRec.Body.String())
	}

	// The seat must be immediately re-holdable by a different buyer.
	rehold := doRequest(t, handler, "POST", "/matches/"+matchID+"/seats/A1/hold", `{"buyer_id":"bob@example.com"}`)
	if rehold.Code != http.StatusCreated {
		t.Errorf("re-hold after cancel status = %d, want 201, body: %s", rehold.Code, rehold.Body.String())
	}
}

func TestHandleHealthz(t *testing.T) {
	handler, _ := testServer(t)

	rec := doRequest(t, handler, "GET", "/healthz", "")
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestRequestBodyTooLarge(t *testing.T) {
	handler, matchID := testServer(t)

	// CODE_REVIEW.md finding #1: bodies over maxRequestBodyBytes must be
	// rejected, not buffered without bound.
	hugeBuyerID := strings.Repeat("a", maxRequestBodyBytes+1)
	body := `{"buyer_id":"` + hugeBuyerID + `"}`

	rec := doRequest(t, handler, "POST", "/matches/"+matchID+"/seats/A1/hold", body)
	if rec.Code == http.StatusCreated {
		t.Error("oversized body was accepted, want rejection")
	}
}
