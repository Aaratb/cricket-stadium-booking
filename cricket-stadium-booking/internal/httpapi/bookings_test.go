package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"stadiumbooking/internal/booking"
)

func TestHandleListBookings_ConfirmedThenCancelled(t *testing.T) {
	handler, matchID := testServer(t)
	buyerID := "alice@example.com"

	holdRec := doRequest(t, handler, http.MethodPost, "/matches/"+matchID+"/seats/A1/hold", `{"buyer_id":"alice@example.com"}`)
	if holdRec.Code != http.StatusCreated {
		t.Fatalf("hold status = %d, body: %s", holdRec.Code, holdRec.Body.String())
	}
	var hold struct {
		HoldID string `json:"hold_id"`
	}
	if err := json.Unmarshal(holdRec.Body.Bytes(), &hold); err != nil {
		t.Fatalf("decode hold: %v", err)
	}

	confirmRec := doRequest(t, handler, http.MethodPost, "/holds/"+hold.HoldID+"/confirm", `{"buyer_id":"alice@example.com"}`)
	if confirmRec.Code != http.StatusOK {
		t.Fatalf("confirm status = %d, body: %s", confirmRec.Code, confirmRec.Body.String())
	}

	path := "/matches/" + matchID + "/bookings?buyer_id=" + url.QueryEscape(buyerID)
	listRec := doRequest(t, handler, http.MethodGet, path, "")
	if listRec.Code != http.StatusOK {
		t.Fatalf("list confirmed status = %d, body: %s", listRec.Code, listRec.Body.String())
	}
	var confirmed bookingsResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &confirmed); err != nil {
		t.Fatalf("decode confirmed list: %v", err)
	}
	if len(confirmed.Bookings) != 1 || confirmed.Bookings[0].Status != "confirmed" {
		t.Fatalf("confirmed list = %+v, want one confirmed booking", confirmed.Bookings)
	}
	if confirmed.Bookings[0].ConfirmedAt == nil || confirmed.Bookings[0].CancelledAt != nil || confirmed.Bookings[0].RefundStatus != nil {
		t.Errorf("confirmed booking metadata = %+v", confirmed.Bookings[0])
	}

	bookingID := confirmed.Bookings[0].ID
	cancelRec := doRequest(t, handler, http.MethodPost, "/bookings/"+hold.HoldID+"/cancel", `{"buyer_id":"alice@example.com"}`)
	if cancelRec.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, body: %s", cancelRec.Code, cancelRec.Body.String())
	}

	listRec = doRequest(t, handler, http.MethodGet, path, "")
	var cancelled struct {
		Bookings []booking.BookingSummary `json:"bookings"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &cancelled); err != nil {
		t.Fatalf("decode cancelled list: %v", err)
	}
	if len(cancelled.Bookings) != 1 {
		t.Fatalf("cancelled list = %+v, want one", cancelled.Bookings)
	}
	got := cancelled.Bookings[0]
	if got.ID != bookingID || got.Status != "cancelled" || got.CancelledAt == nil {
		t.Errorf("cancelled booking = %+v, want booking %d with cancelled_at", got, bookingID)
	}
	if got.RefundStatus == nil || *got.RefundStatus != "pending" {
		t.Errorf("refund status = %v, want pending", got.RefundStatus)
	}

	other := doRequest(t, handler, http.MethodGet, "/matches/"+matchID+"/bookings?buyer_id=bob%40example.com", "")
	var otherResp bookingsResponse
	if err := json.Unmarshal(other.Body.Bytes(), &otherResp); err != nil {
		t.Fatalf("decode other buyer list: %v", err)
	}
	if other.Code != http.StatusOK || len(otherResp.Bookings) != 0 {
		t.Errorf("other buyer status=%d bookings=%+v, want 200 and empty", other.Code, otherResp.Bookings)
	}
}

func TestHandleListBookings_RequiresValidBuyerID(t *testing.T) {
	handler, matchID := testServer(t)

	missing := doRequest(t, handler, http.MethodGet, "/matches/"+matchID+"/bookings", "")
	if missing.Code != http.StatusBadRequest {
		t.Errorf("missing buyer status = %d, want 400", missing.Code)
	}

	tooLong := url.QueryEscape(strings.Repeat("a", maxBuyerIDLen+1))
	invalid := doRequest(t, handler, http.MethodGet, "/matches/"+matchID+"/bookings?buyer_id="+tooLong, "")
	if invalid.Code != http.StatusBadRequest {
		t.Errorf("overlong buyer status = %d, want 400", invalid.Code)
	}
}
