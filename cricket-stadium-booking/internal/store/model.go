package store

import "time"

// BookingRow mirrors the bookings table. Kept independent of the booking
// package's domain types to avoid an import cycle (booking depends on
// store, not the reverse).
type BookingRow struct {
	ID            int64
	MatchID       string
	SeatID        string
	BuyerID       string
	Status        string
	HoldExpiresAt *time.Time
	ConfirmedAt   *time.Time
}

type SeatStatus struct {
	SeatID        string
	Section       string
	Status        string // "available" | "held" | "confirmed"
	HoldExpiresAt *time.Time
}
