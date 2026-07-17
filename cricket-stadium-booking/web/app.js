// Basic seat-picker viewer/demo. Not the correctness proof (that's
// cmd/loadtest) -- this is deliberately simple: no framework, no build
// step, per the Phase 4 design decision.

const MATCH_ID = 'm1';
document.getElementById('match-id-label').textContent = MATCH_ID;

let selectedSeatId = null;
let activeHold = null; // { hold_id, seat_id, hold_expires_at }
let countdownTimer = null;

function buyerId() {
  return document.getElementById('buyer-id').value.trim() || 'anon@example.com';
}

function setStatus(msg) {
  document.getElementById('status').textContent = msg || '';
}

async function refreshSeats() {
  try {
    const res = await fetch(`/matches/${MATCH_ID}/seats`);
    const data = await res.json();
    renderSeats(data.seats || []);
  } catch (e) {
    setStatus('Could not reach server: ' + e);
  }
}

function renderSeats(seats) {
  const bySection = {};
  for (const seat of seats) {
    (bySection[seat.section] ||= []).push(seat);
  }

  const grid = document.getElementById('seat-grid');
  grid.replaceChildren();
  for (const section of Object.keys(bySection).sort()) {
    const div = document.createElement('div');
    div.className = 'section';
    // textContent, not innerHTML: section is server data (currently only
    // from seed fixtures, but this is a latent XSS pattern if a write path
    // for it is ever added -- CODE_REVIEW.md finding #15).
    const heading = document.createElement('h3');
    heading.textContent = section;
    div.appendChild(heading);
    const seatsDiv = document.createElement('div');
    seatsDiv.className = 'seats';

    for (const seat of bySection[section]) {
      const el = document.createElement('div');
      el.textContent = seat.seat_id.split('-').pop();
      el.title = seat.seat_id;

      let cls = seat.status; // available | held | confirmed
      if (seat.status === 'held') {
        cls = (activeHold && activeHold.seat_id === seat.seat_id) ? 'held-mine' : 'held-other';
      }
      el.className = 'seat ' + cls;
      if (seat.seat_id === selectedSeatId) el.classList.add('selected');

      if (seat.status === 'available') {
        // PLATFORM_REVIEW.md: seats must be operable via keyboard, not
        // mouse-only -- role/tabindex/keydown make them act like buttons.
        el.setAttribute('role', 'button');
        el.setAttribute('tabindex', '0');
        el.setAttribute('aria-label', `Seat ${seat.seat_id}, available`);
        el.onclick = () => selectSeat(seat.seat_id);
        el.onkeydown = (e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            selectSeat(seat.seat_id);
          }
        };
      } else {
        el.setAttribute('aria-label', `Seat ${seat.seat_id}, ${seat.status}`);
      }
      seatsDiv.appendChild(el);
    }
    div.appendChild(seatsDiv);
    grid.appendChild(div);
  }
}

function selectSeat(seatId) {
  selectedSeatId = seatId;
  document.getElementById('selection').textContent = 'Selected: ' + seatId;
  document.getElementById('btn-hold').disabled = false;
  refreshSeats();
}

async function holdSeat() {
  if (!selectedSeatId) return;
  try {
    const res = await fetch(`/matches/${MATCH_ID}/seats/${selectedSeatId}/hold`, {
      method: 'POST',
      body: JSON.stringify({ buyer_id: buyerId() }),
    });
    if (res.status === 201) {
      const hold = await res.json();
      activeHold = hold;
      setStatus('');
      showHoldPanel(hold);
    } else {
      setStatus('Seat just taken -- pick another.');
      // Deliberately re-poll immediately here; production would add
      // backoff+jitter (customer-pain-points.md item 3, documented deferred).
      refreshSeats();
    }
  } catch (e) {
    // PLATFORM_REVIEW.md HIGH finding: an unhandled network error here used
    // to leave btn-hold enabled with no message -- a stuck, silent UI.
    setStatus('Network error while holding seat -- please try again.');
  }
}

function showHoldPanel(hold) {
  document.getElementById('hold-panel').style.display = 'block';
  document.getElementById('hold-seat').textContent = hold.seat_id;
  startCountdown(new Date(hold.hold_expires_at));
  refreshSeats();
}

function startCountdown(expiresAt) {
  clearInterval(countdownTimer);
  countdownTimer = setInterval(() => {
    const remainingMs = expiresAt - new Date();
    if (remainingMs <= 0) {
      document.getElementById('countdown').textContent = 'expired';
      document.getElementById('btn-confirm').disabled = true;
      clearInterval(countdownTimer);
      refreshSeats();
      return;
    }
    const m = Math.floor(remainingMs / 60000);
    const s = Math.floor((remainingMs % 60000) / 1000);
    document.getElementById('countdown').textContent =
      `${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
  }, 1000);
}

async function confirmBooking() {
  if (!activeHold) return;
  try {
    const res = await fetch(`/holds/${activeHold.hold_id}/confirm`, {
      method: 'POST',
      body: JSON.stringify({ buyer_id: buyerId() }),
    });
    if (res.status === 200) {
      setStatus('Booking confirmed!');
      resetHoldState();
    } else {
      setStatus('Your hold expired.');
      resetHoldState();
    }
    refreshSeats();
  } catch (e) {
    setStatus('Network error while confirming -- your hold may still be active, please retry.');
  }
}

async function releaseHold() {
  if (!activeHold) return;
  try {
    const res = await fetch(`/holds/${activeHold.hold_id}`, {
      method: 'DELETE',
      body: JSON.stringify({ buyer_id: buyerId() }),
    });
    // PLATFORM_REVIEW.md HIGH finding: resetHoldState() used to run
    // unconditionally here, even on a failed DELETE (404/500) -- silently
    // discarding a hold that might still be active server-side. Only clear
    // local state once the server actually confirms release (204).
    if (res.status === 204) {
      resetHoldState();
      refreshSeats();
    } else {
      setStatus('Could not release hold -- please try again.');
    }
  } catch (e) {
    setStatus('Network error while releasing hold -- please try again.');
  }
}

function resetHoldState() {
  clearInterval(countdownTimer);
  activeHold = null;
  selectedSeatId = null;
  document.getElementById('hold-panel').style.display = 'none';
  document.getElementById('btn-confirm').disabled = false;
  document.getElementById('btn-hold').disabled = true;
  document.getElementById('selection').textContent = 'Selected: none';
}

document.getElementById('btn-hold').onclick = holdSeat;
document.getElementById('btn-confirm').onclick = confirmBooking;
document.getElementById('btn-release').onclick = releaseHold;

refreshSeats();
setInterval(refreshSeats, 2000); // per design/api-contract.md: simplest possible "live" feel
