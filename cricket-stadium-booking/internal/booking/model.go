package booking

import "time"

type Hold struct {
	ID            int64      `json:"hold_id,string"`
	MatchID       string     `json:"match_id"`
	SeatID        string     `json:"seat_id"`
	BuyerID       string     `json:"buyer_id"`
	Status        string     `json:"status"`
	HoldExpiresAt *time.Time `json:"hold_expires_at,omitempty"`
}

type Booking struct {
	ID          int64      `json:"booking_id,string"`
	MatchID     string     `json:"match_id,omitempty"`
	SeatID      string     `json:"seat_id"`
	Status      string     `json:"status"`
	ConfirmedAt *time.Time `json:"confirmed_at,omitempty"`
}

type Seat struct {
	SeatID        string     `json:"seat_id"`
	Section       string     `json:"section"`
	Status        string     `json:"status"`
	HoldExpiresAt *time.Time `json:"hold_expires_at,omitempty"`
}
