-- CODE_REVIEW.md finding #3: ListSeats orders by (section, seat_id) within a
-- match, but the PK on seats(match_id, seat_id) doesn't cover that order,
-- forcing a sort step. Fine at 400 seats; a real cost at thousands.
CREATE INDEX ix_seats_match_section_seat ON seats (match_id, section, seat_id);
