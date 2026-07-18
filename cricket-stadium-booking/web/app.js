// Interactive stadium seat picker. Booking correctness remains server-side;
// this file owns presentation, immediate feedback, and live availability.

const MATCH_ID = 'm1';
const SEAT_REFRESH_INTERVAL_MS = 10_000;
const STADIUM_SECTIONS = ['NORTH', 'SOUTH', 'EAST', 'WEST'];
const LOWER_TIER_SEATS = 60;
const STADIUM_ASPECT_RATIO = 1.68;
const TIER_ROW_COUNTS = {
  lower: [16, 21, 23],
  upper: [19, 21],
};
document.getElementById('match-id-label').textContent = MATCH_ID;

let selectedSeatId = null;
let activeHold = null; // { hold_id, seat_id, hold_expires_at }
let countdownTimer = null;
let refreshPromise = null;
let refreshTimer = null;
let mutationInFlight = false;
let stadiumCentered = false;
let bookingRequestGeneration = 0;
let buyerRefreshTimer = null;
let refundStatusTimer = null;
let cancelTarget = null;
let knownBookings = [];
let knownBookingsBuyer = null;
let confirmedBookingBySeatId = new Map();
const seatElements = new Map();

function buyerId() {
  return document.getElementById('buyer-id').value.trim() || 'anon@example.com';
}

function setStatus(msg, tone = 'error') {
  for (const status of [
    document.getElementById('status'),
    document.getElementById('bookings-status'),
  ]) {
    if (!status) continue;
    status.textContent = msg || '';
    status.dataset.tone = msg ? tone : '';
  }
}

async function refreshSeats() {
  // Reuse an in-flight refresh. In particular, repeated focus/refresh clicks
  // must not create the overlapping GETs that fixed-interval polling did.
  if (refreshPromise) return refreshPromise;
  refreshPromise = (async () => {
    const refreshButton = document.getElementById('btn-refresh');
    refreshButton.setAttribute('aria-busy', 'true');
    try {
      const res = await fetch(`/matches/${MATCH_ID}/seats`);
      if (!res.ok) throw new Error(`server returned ${res.status}`);
      const data = await res.json();
      renderSeats(data.seats || []);
      document.getElementById('last-updated').textContent =
        new Intl.DateTimeFormat(undefined, {
          hour: '2-digit', minute: '2-digit', second: '2-digit',
        }).format(new Date());
    } catch (e) {
      setStatus('Could not reach server: ' + e);
    } finally {
      refreshButton.setAttribute('aria-busy', 'false');
      refreshPromise = null;
    }
  })();
  return refreshPromise;
}

async function refreshAfterMutation() {
  // If a read began before a mutation committed, let it finish and then issue
  // a fresh read. Otherwise its stale response could become the final view.
  if (refreshPromise) await refreshPromise;
  await refreshSeats();
  scheduleNextRefresh();
}

function scheduleNextRefresh() {
  clearTimeout(refreshTimer);
  if (document.visibilityState !== 'visible') return;
  refreshTimer = setTimeout(async () => {
    await refreshSeats();
    scheduleNextRefresh();
  }, SEAT_REFRESH_INTERVAL_MS);
}

async function refreshAndReschedule() {
  clearTimeout(refreshTimer);
  await refreshSeats();
  scheduleNextRefresh();
}

async function refreshAllAndReschedule() {
  clearTimeout(refreshTimer);
  await Promise.all([refreshSeats(), refreshBookings({silent: true})]);
  scheduleNextRefresh();
}

function updateControls() {
  const holdingSelectedSeat = activeHold && activeHold.seat_id === selectedSeatId;
  document.getElementById('btn-hold').disabled =
    mutationInFlight || !selectedSeatId || holdingSelectedSeat;
  document.getElementById('btn-confirm').disabled = mutationInFlight || !activeHold;
  document.getElementById('btn-release').disabled = mutationInFlight || !activeHold;
  document.getElementById('buyer-id').disabled = mutationInFlight || !!activeHold;
  document.getElementById('btn-refresh-bookings').disabled = mutationInFlight;
  for (const button of document.querySelectorAll('.cancel-booking-button')) {
    button.disabled = mutationInFlight;
  }
}

function formatBookingTime(value) {
  if (!value) return '';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '';
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: 'medium', timeStyle: 'short',
  }).format(date);
}

function bookingStatusLabel(booking) {
  if (booking.status === 'confirmed') return 'Confirmed';
  if (booking.refund_status === 'refunded') return 'Refunded';
  if (booking.refund_status === 'pending') return 'Refund pending';
  return 'Cancelled';
}

function confirmedBookingForSeat(seatId) {
  if (knownBookingsBuyer !== buyerId()) return null;
  return confirmedBookingBySeatId.get(seatId) || null;
}

function renderRefundTracker(bookings) {
  const tracker = document.getElementById('refund-tracker');
  const list = document.getElementById('refund-tracker-list');
  const refundBookings = bookings.filter(booking =>
    booking.status === 'cancelled' && booking.refund_status);
  const pending = refundBookings.filter(booking => booking.refund_status === 'pending');
  // The side panel is a compact progress surface, not booking history. Show
  // active refunds, or the latest completed refund as reassurance.
  const visible = (pending.length > 0 ? pending : refundBookings.slice(0, 1)).slice(0, 3);

  list.replaceChildren();
  tracker.hidden = visible.length === 0;
  for (const booking of visible) {
    const item = document.createElement('div');
    item.className = 'refund-tracker-item';
    const seat = document.createElement('span');
    seat.className = 'refund-tracker-seat';
    seat.textContent = booking.seat_id;
    const refund = document.createElement('span');
    refund.className = `refund-status ${booking.refund_status || ''}`;
    refund.textContent = booking.refund_status === 'refunded'
      ? 'Refunded'
      : booking.refund_status === 'failed'
        ? 'Refund needs attention'
        : 'Refund in progress';
    item.append(seat, refund);
    list.appendChild(item);
  }
}

function scheduleRefundStatusRefresh(bookings) {
  clearTimeout(refundStatusTimer);
  if (!bookings.some(booking => booking.refund_status === 'pending')) return;
  refundStatusTimer = setTimeout(() => refreshBookings({silent: true}), 2500);
}

function syncSeatOwnershipForElement(el) {
  const status = el.dataset.status;
  const ownedBooking = status === 'confirmed'
    ? confirmedBookingForSeat(el.dataset.seatId)
    : null;
  el.classList.toggle('owned-confirmed', !!ownedBooking);
  el.disabled = status !== 'available' && !ownedBooking;

  const section = el.dataset.section;
  const number = seatNumber(el.dataset.seatId);
  if (ownedBooking) {
    el.title = `${section} Stand · Seat ${number} · Your confirmed seat · Select to cancel`;
    el.setAttribute('aria-label', `${section} stand, seat ${number}, your confirmed booking; activate to review cancellation`);
  } else {
    el.title = `${section} Stand · Seat ${number} · ${status}`;
    el.setAttribute('aria-label', `${section} stand, seat ${number}, ${status}`);
  }
}

function syncSeatOwnership() {
  for (const el of seatElements.values()) syncSeatOwnershipForElement(el);
}

function renderBookings(bookings) {
  knownBookings = bookings;
  knownBookingsBuyer = buyerId();
  confirmedBookingBySeatId = new Map(bookings
    .filter(booking => booking.status === 'confirmed')
    .map(booking => [booking.seat_id, booking]));
  const list = document.getElementById('bookings-list');
  const empty = document.getElementById('bookings-empty');
  document.getElementById('bookings-buyer').textContent = knownBookingsBuyer;
  document.getElementById('bookings-count').textContent = bookings.length;
  list.replaceChildren();
  empty.hidden = bookings.length > 0;

  for (const booking of bookings) {
    const card = document.createElement('article');
    card.className = `booking-record ${booking.status}`;

    const top = document.createElement('div');
    top.className = 'booking-record-top';
    const seat = document.createElement('strong');
    seat.className = 'booking-seat';
    seat.textContent = booking.seat_id;
    const badge = document.createElement('span');
    badge.className = `status-badge ${booking.status}`;
    badge.textContent = booking.status === 'confirmed' ? 'Confirmed' : 'Cancelled';
    top.append(seat, badge);

    const meta = document.createElement('p');
    meta.className = 'booking-meta';
    const timestamp = booking.status === 'cancelled'
      ? formatBookingTime(booking.cancelled_at)
      : formatBookingTime(booking.confirmed_at);
    meta.textContent = `Booking #${booking.booking_id}${timestamp ? ` · ${timestamp}` : ''}`;

    const footer = document.createElement('div');
    footer.className = 'booking-record-footer';
    if (booking.status === 'confirmed') {
      const copy = document.createElement('span');
      copy.className = 'refund-status refunded';
      copy.textContent = 'Eligible to cancel';
      const cancelButton = document.createElement('button');
      cancelButton.type = 'button';
      cancelButton.className = 'cancel-booking-button';
      cancelButton.textContent = 'Cancel booking';
      cancelButton.onclick = () => openCancelDialog(booking);
      footer.append(copy, cancelButton);
    } else {
      const refund = document.createElement('span');
      refund.className = `refund-status ${booking.refund_status || ''}`;
      refund.textContent = bookingStatusLabel(booking);
      footer.append(refund);
    }

    card.append(top, meta, footer);
    list.appendChild(card);
  }
  renderRefundTracker(bookings);
  syncSeatOwnership();
  scheduleRefundStatusRefresh(bookings);
  updateControls();
}

async function refreshBookings({silent = false} = {}) {
  const requestedBuyer = buyerId();
  const generation = ++bookingRequestGeneration;
  const refreshButton = document.getElementById('btn-refresh-bookings');
  refreshButton.setAttribute('aria-busy', 'true');
  try {
    const params = new URLSearchParams({buyer_id: requestedBuyer});
    const res = await fetch(`/matches/${MATCH_ID}/bookings?${params}`);
    if (!res.ok) throw new Error(`server returned ${res.status}`);
    const data = await res.json();
    if (generation !== bookingRequestGeneration || requestedBuyer !== buyerId()) return;
    renderBookings(data.bookings || []);
  } catch (error) {
    if (!silent && generation === bookingRequestGeneration) {
      setStatus('Could not load your bookings -- please try again.');
    }
  } finally {
    if (generation === bookingRequestGeneration) {
      refreshButton.setAttribute('aria-busy', 'false');
    }
  }
}

function openCancelDialog(booking) {
  cancelTarget = {booking, buyer: buyerId()};
  document.getElementById('cancel-dialog-seat').textContent = booking.seat_id;
  document.getElementById('cancel-dialog').showModal();
  document.getElementById('btn-cancel-dismiss').focus();
}

function closeCancelDialog() {
  if (mutationInFlight) return;
  document.getElementById('cancel-dialog').close();
  cancelTarget = null;
}

async function cancelConfirmedBooking() {
  if (!cancelTarget || mutationInFlight) return;
  const {booking, buyer} = cancelTarget;
  if (buyer !== buyerId()) {
    closeCancelDialog();
    setStatus('Buyer ID changed. Select the booking again before cancelling.');
    return;
  }
  let cancelled = false;
  mutationInFlight = true;
  updateControls();
  document.getElementById('btn-cancel-confirm').disabled = true;
  try {
    const res = await fetch(`/bookings/${booking.booking_id}/cancel`, {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({buyer_id: buyerId()}),
    });
    if (res.status === 200) {
      document.getElementById('cancel-dialog').close();
      cancelTarget = null;
      cancelled = true;
      setStatus(`${booking.seat_id} cancelled. Your refund is being processed.`, 'success');
    } else if (res.status === 409) {
      setStatus('This booking can no longer be cancelled.');
    } else {
      setStatus('Could not cancel this booking -- please try again.');
    }
  } catch (error) {
    setStatus('Network error while cancelling -- please try again.');
  } finally {
    mutationInFlight = false;
    document.getElementById('btn-cancel-confirm').disabled = false;
    updateControls();
    await Promise.all([refreshAfterMutation(), refreshBookings({silent: true})]);
    if (cancelled) setStatus(`${booking.seat_id} cancelled. Track the refund status here.`, 'success');
  }
}

function seatNumber(seatId) {
  const value = Number.parseInt(seatId.split('-').pop(), 10);
  return Number.isNaN(value) ? Number.MAX_SAFE_INTEGER : value;
}

function updateSelectionSummary(seatId) {
  const selection = document.getElementById('selection');
  const hint = document.getElementById('selection-hint');
  if (!seatId) {
    selection.textContent = 'None yet';
    hint.textContent = 'Choose a green seat from the stadium';
    return;
  }
  const [section] = seatId.split('-');
  selection.textContent = seatId;
  hint.textContent = `${section} Stand · Seat ${seatNumber(seatId)}`;
}

function applySeatPlacement(el, section, tierIndex, tier) {
  const rowCounts = TIER_ROW_COUNTS[tier];
  let row = 0;
  let column = tierIndex;
  while (column >= rowCounts[row]) {
    column -= rowCounts[row];
    row++;
  }
  const seatsInRow = rowCounts[row];
  const globalRow = tier === 'lower' ? row : row + TIER_ROW_COUNTS.lower.length;
  const sectionCenters = { EAST: 0, SOUTH: 90, WEST: 180, NORTH: 270 };
  // The first row is the shortest arc, so let it sweep farther into the
  // section corners rather than compressing its chairs at East and West.
  const arcHalfAngle = globalRow === 0 ? 41.5 : 39;
  const angle = sectionCenters[section] - arcHalfAngle
    + column * ((arcHalfAngle * 2) / (seatsInRow - 1));
  const radians = angle * Math.PI / 180;
  // The horizontal and vertical increments resolve to the same physical
  // distance at the stadium's fixed aspect ratio. This prevents side rows
  // from spreading much farther apart than the north/south rows.
  const xRadius = 27.3 + globalRow * 2.9;
  const yRadius = 22.7 + globalRow * 2.9 * STADIUM_ASPECT_RATIO;
  const x = 50 + Math.cos(radians) * xRadius;
  const y = 50 + Math.sin(radians) * yRadius;
  // Tangent of x = rx cos(t), y = ry sin(t), converted to screen units.
  const tangentX = -xRadius * Math.sin(radians);
  const tangentY = (yRadius / STADIUM_ASPECT_RATIO) * Math.cos(radians);
  const tangentAngle = Math.atan2(tangentY, tangentX) * 180 / Math.PI;

  // Every chair belongs to the same elliptical bowl. The cardinal sections
  // are simply four arcs of it, and each chair faces the field tangent.
  el.style.setProperty('--seat-x', `${x}%`);
  el.style.setProperty('--seat-y', `${y}%`);
  el.style.setProperty('--seat-angle', `${tangentAngle}deg`);
  el.style.setProperty('--seat-number-angle', `${-tangentAngle}deg`);
  el.style.setProperty('--seat-layer', `${7 + Math.round(y / 20)}`);
  el.dataset.row = row + 1;
  el.dataset.bowlRow = globalRow + 1;
  el.dataset.tier = tier;
}

function updateSeatElement(el, seat, tierIndex, tier) {
  let visualStatus = seat.status;
  if (seat.status === 'held') {
    visualStatus = activeHold && activeHold.seat_id === seat.seat_id
      ? 'held-mine'
      : 'held-other';
  }

  el.className = `seat ${visualStatus}`;
  if (seat.seat_id === selectedSeatId) el.classList.add('selected');
  let numberLabel = el.querySelector('.seat-number');
  if (!numberLabel) {
    numberLabel = document.createElement('span');
    numberLabel.className = 'seat-number';
    numberLabel.setAttribute('aria-hidden', 'true');
    el.appendChild(numberLabel);
  }
  numberLabel.textContent = seatNumber(seat.seat_id);
  el.dataset.seatId = seat.seat_id;
  el.dataset.section = seat.section;
  el.dataset.status = seat.status;
  syncSeatOwnershipForElement(el);
  applySeatPlacement(el, seat.section, tierIndex, tier);
}

function renderSeats(seats) {
  const bySection = Object.fromEntries(STADIUM_SECTIONS.map((section) => [section, []]));
  const seatById = new Map();
  for (const seat of seats) {
    seatById.set(seat.seat_id, seat);
    const section = seat.section.toUpperCase();
    if (bySection[section]) bySection[section].push({...seat, section});
  }

  if (selectedSeatId) {
    const selectedSeat = seatById.get(selectedSeatId);
    const isOwnHold = activeHold && activeHold.seat_id === selectedSeatId;
    if (!selectedSeat || (selectedSeat.status !== 'available' && !isOwnHold)) {
      selectedSeatId = activeHold ? activeHold.seat_id : null;
      updateSelectionSummary(selectedSeatId);
    }
  }

  const seen = new Set();
  let availableCount = 0;
  for (const section of STADIUM_SECTIONS) {
    const sectionSeats = bySection[section].sort((a, b) =>
      seatNumber(a.seat_id) - seatNumber(b.seat_id));
    let sectionAvailable = 0;

    sectionSeats.forEach((seat, index) => {
      const tier = index < LOWER_TIER_SEATS ? 'lower' : 'upper';
      const tierIndex = tier === 'lower' ? index : index - LOWER_TIER_SEATS;
      const container = document.getElementById(`seats-${section}-${tier}`);
      seen.add(seat.seat_id);
      if (seat.status === 'available') {
        availableCount++;
        sectionAvailable++;
      }

      let el = seatElements.get(seat.seat_id);
      if (!el) {
        el = document.createElement('button');
        el.type = 'button';
        el.onclick = () => handleSeatClick(el.dataset.seatId, el);
        seatElements.set(seat.seat_id, el);
      }
      updateSeatElement(el, seat, tierIndex, tier);
      // appendChild moves an existing element without recreating it, keeping
      // keyboard focus and eliminating the old ten-second refresh flicker.
      container.appendChild(el);
    });

    document.getElementById(`count-${section}`).textContent =
      `${sectionAvailable}/${sectionSeats.length} open`;
  }

  for (const [seatId, el] of seatElements) {
    if (!seen.has(seatId)) {
      el.remove();
      seatElements.delete(seatId);
    }
  }
  document.getElementById('available-count').textContent = availableCount;
  updateControls();

  if (!stadiumCentered) {
    requestAnimationFrame(() => {
      const scroller = document.querySelector('.stadium-scroller');
      if (scroller.scrollWidth > scroller.clientWidth) {
        scroller.scrollLeft = (scroller.scrollWidth - scroller.clientWidth) / 2;
      }
      stadiumCentered = true;
    });
  }
}

function handleSeatClick(seatId, seatElement) {
  if (seatElement.dataset.status === 'confirmed') {
    const booking = confirmedBookingForSeat(seatId);
    if (booking) openCancelDialog(booking);
    return;
  }
  selectSeat(seatId, seatElement);
}

function selectSeat(seatId, seatElement) {
  selectedSeatId = seatId;
  for (const selected of document.querySelectorAll('.seat.selected')) {
    selected.classList.remove('selected');
  }
  seatElement.classList.add('selected');
  updateSelectionSummary(seatId);
  setStatus('');
  updateControls();
}

async function holdSeat() {
  if (!selectedSeatId || mutationInFlight) return;
  const targetSeatId = selectedSeatId;
  const replacingSeatId = activeHold && activeHold.seat_id !== targetSeatId
    ? activeHold.seat_id
    : null;
  mutationInFlight = true;
  updateControls();
  try {
    const res = await fetch(`/matches/${MATCH_ID}/seats/${targetSeatId}/hold`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ buyer_id: buyerId() }),
    });
    if (res.status === 201) {
      const hold = await res.json();
      activeHold = hold;
      selectedSeatId = hold.seat_id;
      setStatus(replacingSeatId
        ? `Released ${replacingSeatId}; ${hold.seat_id} is now held.`
        : `${hold.seat_id} is held for you.`, 'success');
      updateSelectionSummary(hold.seat_id);
      showHoldPanel(hold);
    } else {
      setStatus('Seat just taken -- pick another.');
      if (activeHold) {
        // The server replaces holds atomically, so a failed replacement means
        // the previous hold is still valid. Put the selection back on it.
        selectedSeatId = activeHold.seat_id;
        updateSelectionSummary(activeHold.seat_id);
      }
    }
  } catch (e) {
    // PLATFORM_REVIEW.md HIGH finding: an unhandled network error here used
    // to leave btn-hold enabled with no message -- a stuck, silent UI.
    setStatus('Network error while holding seat -- please try again.');
  } finally {
    mutationInFlight = false;
    updateControls();
    await refreshAfterMutation();
  }
}

function showHoldPanel(hold) {
  document.getElementById('hold-panel').hidden = false;
  document.getElementById('hold-seat').textContent = hold.seat_id;
  startCountdown(new Date(hold.hold_expires_at));
  updateControls();
}

function startCountdown(expiresAt) {
  clearInterval(countdownTimer);
  const renderCountdown = () => {
    const remainingMs = expiresAt - new Date();
    if (remainingMs <= 0) {
      clearInterval(countdownTimer);
      setStatus('Your hold expired.');
      resetHoldState();
      refreshAfterMutation();
      return false;
    }
    const m = Math.floor(remainingMs / 60000);
    const s = Math.floor((remainingMs % 60000) / 1000);
    document.getElementById('countdown').textContent =
      `${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
    return true;
  };
  if (renderCountdown()) countdownTimer = setInterval(renderCountdown, 1000);
}

async function confirmBooking() {
  if (!activeHold || mutationInFlight) return;
  const hold = activeHold;
  mutationInFlight = true;
  updateControls();
  try {
    const res = await fetch(`/holds/${hold.hold_id}/confirm`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ buyer_id: hold.buyer_id }),
    });
    if (res.status === 200) {
      const booking = await res.json();
      setStatus(`Booking confirmed for ${hold.seat_id}.`, 'success');
      resetHoldState();
      // The confirmation response contains the booking ID needed by the
      // cancellation API. Reload the buyer's bookings immediately so the
      // newly confirmed ticket and its Cancel action are never lost.
      renderBookings([
        booking,
        ...knownBookings.filter(item => item.booking_id !== booking.booking_id),
      ]);
      await refreshBookings({silent: true});
    } else {
      setStatus('Your hold expired.');
      resetHoldState();
    }
  } catch (e) {
    setStatus('Network error while confirming -- your hold may still be active, please retry.');
  } finally {
    mutationInFlight = false;
    updateControls();
    await refreshAfterMutation();
  }
}

async function releaseHold() {
  if (!activeHold || mutationInFlight) return;
  const hold = activeHold;
  mutationInFlight = true;
  updateControls();
  try {
    const res = await fetch(`/holds/${hold.hold_id}`, {
      method: 'DELETE',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ buyer_id: hold.buyer_id }),
    });
    // PLATFORM_REVIEW.md HIGH finding: resetHoldState() used to run
    // unconditionally here, even on a failed DELETE (404/500) -- silently
    // discarding a hold that might still be active server-side. Only clear
    // local state once the server actually confirms release (204).
    if (res.status === 204) {
      resetHoldState();
      setStatus(`Released ${hold.seat_id}.`, 'success');
    } else {
      setStatus('Could not release hold -- please try again.');
    }
  } catch (e) {
    setStatus('Network error while releasing hold -- please try again.');
  } finally {
    mutationInFlight = false;
    updateControls();
    await refreshAfterMutation();
  }
}

function resetHoldState() {
  clearInterval(countdownTimer);
  activeHold = null;
  selectedSeatId = null;
  document.getElementById('hold-panel').hidden = true;
  for (const selected of document.querySelectorAll('.seat.selected')) {
    selected.classList.remove('selected');
  }
  updateSelectionSummary(null);
  updateControls();
}

function switchAppTab(tabName, {refresh = true} = {}) {
  const showBookings = tabName === 'bookings';
  const seatMapTab = document.getElementById('tab-seat-map');
  const bookingsTab = document.getElementById('tab-my-bookings');
  document.getElementById('seat-map-view').hidden = showBookings;
  document.getElementById('my-bookings-view').hidden = !showBookings;
  seatMapTab.setAttribute('aria-selected', String(!showBookings));
  bookingsTab.setAttribute('aria-selected', String(showBookings));
  seatMapTab.tabIndex = showBookings ? -1 : 0;
  bookingsTab.tabIndex = showBookings ? 0 : -1;
  if (showBookings && refresh) refreshBookings({silent: true});
}

function handleTabKeydown(event) {
  if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
  event.preventDefault();
  const goToBookings = event.key === 'ArrowRight' || event.key === 'End';
  switchAppTab(goToBookings ? 'bookings' : 'map');
  document.getElementById(goToBookings ? 'tab-my-bookings' : 'tab-seat-map').focus();
}

document.getElementById('btn-hold').onclick = holdSeat;
document.getElementById('btn-confirm').onclick = confirmBooking;
document.getElementById('btn-release').onclick = releaseHold;
document.getElementById('btn-refresh').onclick = refreshAllAndReschedule;
document.getElementById('btn-refresh-bookings').onclick = () => refreshBookings();
document.getElementById('btn-cancel-dismiss').onclick = closeCancelDialog;
document.getElementById('btn-cancel-confirm').onclick = cancelConfirmedBooking;
document.getElementById('tab-seat-map').onclick = () => switchAppTab('map');
document.getElementById('tab-my-bookings').onclick = () => switchAppTab('bookings');
document.getElementById('tab-seat-map').onkeydown = handleTabKeydown;
document.getElementById('tab-my-bookings').onkeydown = handleTabKeydown;
document.getElementById('btn-change-buyer').onclick = () => {
  switchAppTab('map', {refresh: false});
  document.getElementById('buyer-id').focus();
};
document.getElementById('cancel-dialog').addEventListener('close', () => {
  if (!mutationInFlight) cancelTarget = null;
});
document.getElementById('cancel-dialog').addEventListener('cancel', event => {
  if (mutationInFlight) event.preventDefault();
});
document.getElementById('buyer-id').addEventListener('input', () => {
  if (cancelTarget && !mutationInFlight) closeCancelDialog();
  knownBookings = [];
  knownBookingsBuyer = null;
  confirmedBookingBySeatId = new Map();
  renderBookings([]);
  clearTimeout(buyerRefreshTimer);
  buyerRefreshTimer = setTimeout(() => refreshBookings(), 350);
});

document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'visible') {
    refreshAndReschedule();
  } else {
    clearTimeout(refreshTimer);
  }
});

refreshAndReschedule();
refreshBookings({silent: true});
switchAppTab('map', {refresh: false});
