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

// The script is loaded at the end of <body>, so cache stable DOM references
// once instead of resolving the same IDs on every interaction and poll.
const dom = {
  matchIdLabel: document.getElementById('match-id-label'),
  buyerInput: document.getElementById('buyer-id'),
  status: document.getElementById('status'),
  bookingsStatus: document.getElementById('bookings-status'),
  refreshSeatsButton: document.getElementById('btn-refresh'),
  refreshBookingsButton: document.getElementById('btn-refresh-bookings'),
  holdButton: document.getElementById('btn-hold'),
  confirmButton: document.getElementById('btn-confirm'),
  releaseButton: document.getElementById('btn-release'),
  lastUpdated: document.getElementById('last-updated'),
  availableCount: document.getElementById('available-count'),
  selection: document.getElementById('selection'),
  selectionHint: document.getElementById('selection-hint'),
  holdPanel: document.getElementById('hold-panel'),
  holdSeat: document.getElementById('hold-seat'),
  countdown: document.getElementById('countdown'),
  stadiumScroller: document.querySelector('.stadium-scroller'),
  bookingsList: document.getElementById('bookings-list'),
  bookingsEmpty: document.getElementById('bookings-empty'),
  bookingsBuyer: document.getElementById('bookings-buyer'),
  bookingsCount: document.getElementById('bookings-count'),
  refundTracker: document.getElementById('refund-tracker'),
  refundTrackerList: document.getElementById('refund-tracker-list'),
  seatMapTab: document.getElementById('tab-seat-map'),
  bookingsTab: document.getElementById('tab-my-bookings'),
  seatMapView: document.getElementById('seat-map-view'),
  bookingsView: document.getElementById('my-bookings-view'),
  changeBuyerButton: document.getElementById('btn-change-buyer'),
  cancelDialog: document.getElementById('cancel-dialog'),
  cancelDialogSeat: document.getElementById('cancel-dialog-seat'),
  cancelDismissButton: document.getElementById('btn-cancel-dismiss'),
  cancelConfirmButton: document.getElementById('btn-cancel-confirm'),
};
dom.seatContainers = new Map();
dom.sectionCounts = new Map();
for (const section of STADIUM_SECTIONS) {
  dom.sectionCounts.set(section, document.getElementById(`count-${section}`));
  for (const tier of Object.keys(TIER_ROW_COUNTS)) {
    dom.seatContainers.set(`${section}-${tier}`, document.getElementById(`seats-${section}-${tier}`));
  }
}
dom.matchIdLabel.textContent = MATCH_ID;

const lastUpdatedFormatter = new Intl.DateTimeFormat(undefined, {
  hour: '2-digit', minute: '2-digit', second: '2-digit',
});
const bookingTimeFormatter = new Intl.DateTimeFormat(undefined, {
  dateStyle: 'medium', timeStyle: 'short',
});

let selectedSeatId = null;
let activeHold = null; // { hold_id, seat_id, hold_expires_at }
let countdownTimer = null;
let refreshPromise = null;
let refreshTimer = null;
let mutationInFlight = false;
let stadiumCentered = false;
let bookingRequestGeneration = 0;
let bookingRequest = null;
let buyerRefreshTimer = null;
let refundStatusTimer = null;
let cancelTarget = null;
let knownBookings = [];
let knownBookingsBuyer = null;
let confirmedBookingBySeatId = new Map();
const seatElements = new Map();
const seatNumberCache = new Map();
const seatPlacementCache = new Map();
const bookingById = new Map();
const bookingCards = new Map();
const cancelBookingButtons = new Set();
const refundTrackerItems = new Map();

function setText(element, value) {
  const text = String(value);
  if (element.textContent !== text) element.textContent = text;
}

function setDisabled(element, disabled) {
  if (element.disabled !== disabled) element.disabled = disabled;
}

function setHidden(element, hidden) {
  if (element.hidden !== hidden) element.hidden = hidden;
}

function setAriaBusy(element, busy) {
  const value = String(busy);
  if (element.getAttribute('aria-busy') !== value) element.setAttribute('aria-busy', value);
}

function setAttributeIfChanged(element, name, value) {
  if (element.getAttribute(name) !== value) element.setAttribute(name, value);
}

function buyerId() {
  return dom.buyerInput.value.trim() || 'anon@example.com';
}

function setStatus(msg, tone = 'error') {
  for (const status of [dom.status, dom.bookingsStatus]) {
    if (!status) continue;
    setText(status, msg || '');
    const nextTone = msg ? tone : '';
    if (status.dataset.tone !== nextTone) status.dataset.tone = nextTone;
  }
}

async function refreshSeats() {
  // Reuse an in-flight refresh. In particular, repeated focus/refresh clicks
  // must not create the overlapping GETs that fixed-interval polling did.
  if (refreshPromise) return refreshPromise;
  refreshPromise = (async () => {
    setAriaBusy(dom.refreshSeatsButton, true);
    try {
      // Use the browser's normal HTTP cache semantics. When the server sends
      // validators, fetch will revalidate and reuse the cached body without
      // application code maintaining a second ETag cache.
      const res = await fetch(`/matches/${MATCH_ID}/seats`);
      if (!res.ok) throw new Error(`server returned ${res.status}`);
      const data = await res.json();
      renderSeats(data.seats || []);
      setText(dom.lastUpdated, lastUpdatedFormatter.format(new Date()));
    } catch (e) {
      setStatus('Could not reach server: ' + e);
    } finally {
      setAriaBusy(dom.refreshSeatsButton, false);
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
  setDisabled(dom.holdButton, mutationInFlight || !selectedSeatId || !!holdingSelectedSeat);
  setDisabled(dom.confirmButton, mutationInFlight || !activeHold);
  setDisabled(dom.releaseButton, mutationInFlight || !activeHold);
  setDisabled(dom.buyerInput, mutationInFlight || !!activeHold);
  setDisabled(dom.refreshBookingsButton, mutationInFlight);
  for (const button of cancelBookingButtons) setDisabled(button, mutationInFlight);
}

function formatBookingTime(value) {
  if (!value) return '';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '';
  return bookingTimeFormatter.format(date);
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

function reconcileOrderedChildren(container, desiredNodes) {
  if (desiredNodes.length > 0 && !container.firstElementChild) {
    const fragment = document.createDocumentFragment();
    for (const node of desiredNodes) fragment.appendChild(node);
    container.appendChild(fragment);
    return;
  }

  let cursor = container.firstElementChild;
  for (const node of desiredNodes) {
    if (node === cursor) {
      cursor = cursor.nextElementSibling;
      continue;
    }
    // This branch runs only when an item is new or the server's order really
    // changed. An unchanged poll performs no append/insert operations.
    container.insertBefore(node, cursor);
  }
}

function refundLabel(status) {
  if (status === 'refunded') return 'Refunded';
  if (status === 'failed') return 'Refund needs attention';
  return 'Refund in progress';
}

function renderRefundTracker(bookings) {
  const refundBookings = bookings.filter(booking =>
    booking.status === 'cancelled' && booking.refund_status);
  const pending = refundBookings.filter(booking => booking.refund_status === 'pending');
  // The side panel is a compact progress surface, not booking history. Show
  // active refunds, or the latest completed refund as reassurance.
  const visible = (pending.length > 0 ? pending : refundBookings.slice(0, 1)).slice(0, 3);
  const visibleKeys = new Set(visible.map(booking => String(booking.booking_id)));
  for (const [key, record] of refundTrackerItems) {
    if (visibleKeys.has(key)) continue;
    record.element.remove();
    refundTrackerItems.delete(key);
  }

  const desiredNodes = [];
  for (const booking of visible) {
    const key = String(booking.booking_id);
    const signature = `${booking.seat_id}\u0000${booking.refund_status || ''}`;
    let record = refundTrackerItems.get(key);
    if (!record) {
      const element = document.createElement('div');
      element.className = 'refund-tracker-item';
      const seat = document.createElement('span');
      seat.className = 'refund-tracker-seat';
      const refund = document.createElement('span');
      element.append(seat, refund);
      record = {element, seat, refund, signature: null};
      refundTrackerItems.set(key, record);
    }
    if (record.signature !== signature) {
      setText(record.seat, booking.seat_id);
      const refundClass = `refund-status ${booking.refund_status || ''}`;
      if (record.refund.className !== refundClass) record.refund.className = refundClass;
      setText(record.refund, refundLabel(booking.refund_status));
      record.signature = signature;
    }
    desiredNodes.push(record.element);
  }
  reconcileOrderedChildren(dom.refundTrackerList, desiredNodes);
  setHidden(dom.refundTracker, visible.length === 0);
}

function hasPendingRefunds(bookings = knownBookings) {
  return bookings.some(booking => booking.refund_status === 'pending');
}

function scheduleRefundStatusRefresh(bookings = knownBookings) {
  clearTimeout(refundStatusTimer);
  refundStatusTimer = null;
  if (document.visibilityState !== 'visible' || !hasPendingRefunds(bookings)) return;
  refundStatusTimer = setTimeout(async () => {
    refundStatusTimer = null;
    await refreshBookings({silent: true});
    // Successful responses schedule from renderBookings. On a transient
    // failure, keep checking while the page is visible instead of silently
    // abandoning the refund tracker.
    if (!refundStatusTimer) scheduleRefundStatusRefresh();
  }, 2500);
}

function syncSeatOwnershipForElement(el) {
  const status = el.dataset.status;
  const ownedBooking = status === 'confirmed'
    ? confirmedBookingForSeat(el.dataset.seatId)
    : null;
  const visualStatus = status === 'held'
    ? activeHold && activeHold.seat_id === el.dataset.seatId ? 'held-mine' : 'held-other'
    : status;
  const classes = ['seat', visualStatus];
  if (el.dataset.seatId === selectedSeatId) classes.push('selected');
  if (ownedBooking) classes.push('owned-confirmed');
  const className = classes.join(' ');
  if (el.className !== className) el.className = className;
  setDisabled(el, status !== 'available' && !ownedBooking);

  const section = el.dataset.section;
  const number = seatNumber(el.dataset.seatId);
  if (ownedBooking) {
    const title = `${section} Stand · Seat ${number} · Your confirmed seat · Select to cancel`;
    if (el.title !== title) el.title = title;
    setAttributeIfChanged(el, 'aria-label', `${section} stand, seat ${number}, your confirmed booking; activate to review cancellation`);
  } else {
    const title = `${section} Stand · Seat ${number} · ${status}`;
    if (el.title !== title) el.title = title;
    setAttributeIfChanged(el, 'aria-label', `${section} stand, seat ${number}, ${status}`);
  }
}

function syncChangedSeatOwnership(previousMap, nextMap, buyerChanged) {
  const affectedSeatIds = new Set();
  if (buyerChanged) {
    for (const seatId of previousMap.keys()) affectedSeatIds.add(seatId);
    for (const seatId of nextMap.keys()) affectedSeatIds.add(seatId);
  } else {
    for (const seatId of previousMap.keys()) {
      if (!nextMap.has(seatId)) affectedSeatIds.add(seatId);
    }
    for (const seatId of nextMap.keys()) {
      if (!previousMap.has(seatId)) affectedSeatIds.add(seatId);
    }
  }
  for (const seatId of affectedSeatIds) {
    const element = seatElements.get(seatId);
    if (element) syncSeatOwnershipForElement(element);
  }
}

function bookingSignature(booking) {
  return [
    booking.seat_id,
    booking.status,
    booking.refund_status || '',
    booking.confirmed_at || '',
    booking.cancelled_at || '',
  ].join('\u0000');
}

function createBookingCard(booking, signature) {
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
  let cancelButton = null;
  if (booking.status === 'confirmed') {
    const copy = document.createElement('span');
    copy.className = 'refund-status refunded';
    copy.textContent = 'Eligible to cancel';
    cancelButton = document.createElement('button');
    cancelButton.type = 'button';
    cancelButton.className = 'cancel-booking-button';
    cancelButton.dataset.bookingId = String(booking.booking_id);
    cancelButton.textContent = 'Cancel booking';
    setDisabled(cancelButton, mutationInFlight);
    cancelBookingButtons.add(cancelButton);
    footer.append(copy, cancelButton);
  } else {
    const refund = document.createElement('span');
    refund.className = `refund-status ${booking.refund_status || ''}`;
    refund.textContent = bookingStatusLabel(booking);
    footer.append(refund);
  }

  card.append(top, meta, footer);
  return {element: card, cancelButton, signature};
}

function renderBookings(bookings, {buyer = buyerId()} = {}) {
  const previousConfirmedBookings = confirmedBookingBySeatId;
  const previousBuyer = knownBookingsBuyer;
  knownBookings = bookings;
  knownBookingsBuyer = buyer;
  const nextConfirmedBookings = new Map(bookings
    .filter(booking => booking.status === 'confirmed')
    .map(booking => [booking.seat_id, booking]));
  confirmedBookingBySeatId = nextConfirmedBookings;

  bookingById.clear();
  const desiredKeys = new Set();
  const desiredNodes = [];

  for (const booking of bookings) {
    const key = String(booking.booking_id);
    const signature = bookingSignature(booking);
    desiredKeys.add(key);
    bookingById.set(key, booking);
    let record = bookingCards.get(key);
    if (!record || record.signature !== signature) {
      const replacement = createBookingCard(booking, signature);
      if (record) {
        if (record.cancelButton) cancelBookingButtons.delete(record.cancelButton);
        record.element.replaceWith(replacement.element);
      }
      record = replacement;
      bookingCards.set(key, record);
    }
    desiredNodes.push(record.element);
  }

  for (const [key, record] of bookingCards) {
    if (desiredKeys.has(key)) continue;
    if (record.cancelButton) cancelBookingButtons.delete(record.cancelButton);
    record.element.remove();
    bookingCards.delete(key);
  }
  reconcileOrderedChildren(dom.bookingsList, desiredNodes);
  setText(dom.bookingsBuyer, buyer);
  setText(dom.bookingsCount, bookings.length);
  setHidden(dom.bookingsEmpty, bookings.length > 0);
  renderRefundTracker(bookings);
  syncChangedSeatOwnership(
    previousConfirmedBookings,
    nextConfirmedBookings,
    previousBuyer !== buyer,
  );
  scheduleRefundStatusRefresh(bookings);
  updateControls();
}

function abortBookingRefresh() {
  if (!bookingRequest) return;
  const request = bookingRequest;
  bookingRequest = null;
  bookingRequestGeneration++;
  request.controller.abort();
  setAriaBusy(dom.refreshBookingsButton, false);
}

function refreshBookings({silent = false, force = false} = {}) {
  const requestedBuyer = buyerId();
  if (bookingRequest && bookingRequest.buyer === requestedBuyer && !force) {
    if (!silent) bookingRequest.reportErrors = true;
    return bookingRequest.promise;
  }
  if (bookingRequest) abortBookingRefresh();

  const generation = ++bookingRequestGeneration;
  const controller = new AbortController();
  const request = {
    buyer: requestedBuyer,
    controller,
    generation,
    reportErrors: !silent,
    promise: null,
  };
  bookingRequest = request;
  setAriaBusy(dom.refreshBookingsButton, true);
  request.promise = (async () => {
    try {
      const params = new URLSearchParams({buyer_id: requestedBuyer});
      const res = await fetch(`/matches/${MATCH_ID}/bookings?${params}`, {
        signal: controller.signal,
      });
      if (!res.ok) throw new Error(`server returned ${res.status}`);
      const data = await res.json();
      if (bookingRequest !== request || generation !== bookingRequestGeneration || requestedBuyer !== buyerId()) return;
      renderBookings(data.bookings || [], {buyer: requestedBuyer});
    } catch (error) {
      if (error.name !== 'AbortError' && request.reportErrors && bookingRequest === request) {
        setStatus('Could not load your bookings -- please try again.');
      }
    } finally {
      if (bookingRequest === request) {
        bookingRequest = null;
        setAriaBusy(dom.refreshBookingsButton, false);
      }
    }
  })();
  return request.promise;
}

function openCancelDialog(booking) {
  cancelTarget = {booking, buyer: buyerId()};
  setText(dom.cancelDialogSeat, booking.seat_id);
  dom.cancelDialog.showModal();
  dom.cancelDismissButton.focus();
}

function closeCancelDialog() {
  if (mutationInFlight) return;
  dom.cancelDialog.close();
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
  setDisabled(dom.cancelConfirmButton, true);
  try {
    const res = await fetch(`/bookings/${booking.booking_id}/cancel`, {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({buyer_id: buyerId()}),
    });
    if (res.status === 200) {
      dom.cancelDialog.close();
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
    setDisabled(dom.cancelConfirmButton, false);
    updateControls();
    await Promise.all([
      refreshAfterMutation(),
      refreshBookings({silent: true, force: true}),
    ]);
    if (cancelled) setStatus(`${booking.seat_id} cancelled. Track the refund status here.`, 'success');
  }
}

function seatNumber(seatId) {
  if (seatNumberCache.has(seatId)) return seatNumberCache.get(seatId);
  const value = Number.parseInt(seatId.split('-').pop(), 10);
  const number = Number.isNaN(value) ? Number.MAX_SAFE_INTEGER : value;
  seatNumberCache.set(seatId, number);
  return number;
}

function updateSelectionSummary(seatId) {
  if (!seatId) {
    setText(dom.selection, 'None yet');
    setText(dom.selectionHint, 'Choose a green seat from the stadium');
    return;
  }
  const [section] = seatId.split('-');
  setText(dom.selection, seatId);
  setText(dom.selectionHint, `${section} Stand · Seat ${seatNumber(seatId)}`);
}

function applySeatPlacement(el, section, tierIndex, tier) {
  const placementKey = `${section}-${tier}-${tierIndex}`;
  if (el.dataset.placementKey === placementKey) return;

  let placement = seatPlacementCache.get(placementKey);
  if (!placement) {
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

    placement = {
      x: `${x}%`,
      y: `${y}%`,
      angle: `${tangentAngle}deg`,
      numberAngle: `${-tangentAngle}deg`,
      layer: `${7 + Math.round(y / 20)}`,
      row: String(row + 1),
      bowlRow: String(globalRow + 1),
      tier,
    };
    seatPlacementCache.set(placementKey, placement);
  }

  // Every chair belongs to the same elliptical bowl. The cardinal sections
  // are simply four arcs of it, and each chair faces the field tangent. These
  // values are immutable for the normal polling path and are written once.
  el.style.setProperty('--seat-x', placement.x);
  el.style.setProperty('--seat-y', placement.y);
  el.style.setProperty('--seat-angle', placement.angle);
  el.style.setProperty('--seat-number-angle', placement.numberAngle);
  el.style.setProperty('--seat-layer', placement.layer);
  el.dataset.row = placement.row;
  el.dataset.bowlRow = placement.bowlRow;
  el.dataset.tier = placement.tier;
  el.dataset.placementKey = placementKey;
}

function updateSeatElement(el, seat, section, tierIndex, tier) {
  let numberLabel = el._seatNumberLabel;
  if (!numberLabel) {
    numberLabel = document.createElement('span');
    numberLabel.className = 'seat-number';
    numberLabel.setAttribute('aria-hidden', 'true');
    el.appendChild(numberLabel);
    el._seatNumberLabel = numberLabel;
  }
  setText(numberLabel, seatNumber(seat.seat_id));

  let presentationChanged = false;
  if (el.dataset.seatId !== seat.seat_id) {
    el.dataset.seatId = seat.seat_id;
    presentationChanged = true;
  }
  if (el.dataset.section !== section) {
    el.dataset.section = section;
    presentationChanged = true;
  }
  if (el.dataset.status !== seat.status) {
    el.dataset.status = seat.status;
    presentationChanged = true;
  }
  applySeatPlacement(el, section, tierIndex, tier);
  if (presentationChanged) syncSeatOwnershipForElement(el);
}

function renderSeats(seats) {
  const bySection = Object.fromEntries(STADIUM_SECTIONS.map((section) => [section, []]));
  const seatById = new Map();
  for (const seat of seats) {
    seatById.set(seat.seat_id, seat);
    const section = seat.section.toUpperCase();
    if (bySection[section]) bySection[section].push(seat);
  }

  if (selectedSeatId) {
    const selectedSeat = seatById.get(selectedSeatId);
    const isOwnHold = activeHold && activeHold.seat_id === selectedSeatId;
    if (!selectedSeat || (selectedSeat.status !== 'available' && !isOwnHold)) {
      setSelectedSeatState(activeHold ? activeHold.seat_id : null);
    }
  }

  const seen = new Set();
  const desiredByContainer = new Map();
  for (const key of dom.seatContainers.keys()) desiredByContainer.set(key, []);
  let availableCount = 0;
  for (const section of STADIUM_SECTIONS) {
    const sectionSeats = bySection[section].sort((a, b) =>
      seatNumber(a.seat_id) - seatNumber(b.seat_id));
    let sectionAvailable = 0;

    sectionSeats.forEach((seat, index) => {
      const tier = index < LOWER_TIER_SEATS ? 'lower' : 'upper';
      const tierIndex = tier === 'lower' ? index : index - LOWER_TIER_SEATS;
      seen.add(seat.seat_id);
      if (seat.status === 'available') {
        availableCount++;
        sectionAvailable++;
      }

      let el = seatElements.get(seat.seat_id);
      if (!el) {
        el = document.createElement('button');
        el.type = 'button';
        seatElements.set(seat.seat_id, el);
      }
      updateSeatElement(el, seat, section, tierIndex, tier);
      desiredByContainer.get(`${section}-${tier}`).push(el);
    });

    setText(dom.sectionCounts.get(section), `${sectionAvailable}/${sectionSeats.length} open`);
  }

  for (const [seatId, el] of seatElements) {
    if (!seen.has(seatId)) {
      el.remove();
      seatElements.delete(seatId);
      seatNumberCache.delete(seatId);
    }
  }
  for (const [key, desiredNodes] of desiredByContainer) {
    reconcileOrderedChildren(dom.seatContainers.get(key), desiredNodes);
  }
  setText(dom.availableCount, availableCount);
  updateControls();

  if (!stadiumCentered) {
    requestAnimationFrame(() => {
      if (dom.stadiumScroller.scrollWidth > dom.stadiumScroller.clientWidth) {
        dom.stadiumScroller.scrollLeft =
          (dom.stadiumScroller.scrollWidth - dom.stadiumScroller.clientWidth) / 2;
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

function setSelectedSeatState(seatId) {
  const previousSeatId = selectedSeatId;
  selectedSeatId = seatId;
  if (previousSeatId !== seatId) {
    const previousElement = seatElements.get(previousSeatId);
    if (previousElement) syncSeatOwnershipForElement(previousElement);
    const nextElement = seatElements.get(seatId);
    if (nextElement) syncSeatOwnershipForElement(nextElement);
  }
  updateSelectionSummary(seatId);
}

function selectSeat(seatId, seatElement) {
  setSelectedSeatState(seatId);
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
      setSelectedSeatState(hold.seat_id);
      setStatus(replacingSeatId
        ? `Released ${replacingSeatId}; ${hold.seat_id} is now held.`
        : `${hold.seat_id} is held for you.`, 'success');
      showHoldPanel(hold);
    } else {
      setStatus('Seat just taken -- pick another.');
      if (activeHold) {
        // The server replaces holds atomically, so a failed replacement means
        // the previous hold is still valid. Put the selection back on it.
        setSelectedSeatState(activeHold.seat_id);
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
  setHidden(dom.holdPanel, false);
  setText(dom.holdSeat, hold.seat_id);
  startCountdown(new Date(hold.hold_expires_at));
  updateControls();
}

function startCountdown(expiresAt) {
  clearInterval(countdownTimer);
  const expiresAtMs = expiresAt.getTime();
  const renderCountdown = () => {
    const remainingMs = expiresAtMs - Date.now();
    if (remainingMs <= 0) {
      clearInterval(countdownTimer);
      setStatus('Your hold expired.');
      resetHoldState();
      refreshAfterMutation();
      return false;
    }
    const m = Math.floor(remainingMs / 60000);
    const s = Math.floor((remainingMs % 60000) / 1000);
    setText(dom.countdown, `${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`);
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
      await refreshBookings({silent: true, force: true});
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
  // Remove the selection while activeHold is still set so the held chair
  // keeps its current colour until the authoritative seat refresh arrives.
  setSelectedSeatState(null);
  activeHold = null;
  setHidden(dom.holdPanel, true);
  updateControls();
}

function switchAppTab(tabName, {refresh = true} = {}) {
  const showBookings = tabName === 'bookings';
  setHidden(dom.seatMapView, showBookings);
  setHidden(dom.bookingsView, !showBookings);
  setAttributeIfChanged(dom.seatMapTab, 'aria-selected', String(!showBookings));
  setAttributeIfChanged(dom.bookingsTab, 'aria-selected', String(showBookings));
  const seatMapTabIndex = showBookings ? -1 : 0;
  const bookingsTabIndex = showBookings ? 0 : -1;
  if (dom.seatMapTab.tabIndex !== seatMapTabIndex) dom.seatMapTab.tabIndex = seatMapTabIndex;
  if (dom.bookingsTab.tabIndex !== bookingsTabIndex) dom.bookingsTab.tabIndex = bookingsTabIndex;
  if (showBookings && refresh) refreshBookings({silent: true});
}

function handleTabKeydown(event) {
  if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
  event.preventDefault();
  const goToBookings = event.key === 'ArrowRight' || event.key === 'End';
  switchAppTab(goToBookings ? 'bookings' : 'map');
  (goToBookings ? dom.bookingsTab : dom.seatMapTab).focus();
}

dom.holdButton.onclick = holdSeat;
dom.confirmButton.onclick = confirmBooking;
dom.releaseButton.onclick = releaseHold;
dom.refreshSeatsButton.onclick = refreshAllAndReschedule;
dom.refreshBookingsButton.onclick = () => refreshBookings();
dom.cancelDismissButton.onclick = closeCancelDialog;
dom.cancelConfirmButton.onclick = cancelConfirmedBooking;
dom.seatMapTab.onclick = () => switchAppTab('map');
dom.bookingsTab.onclick = () => switchAppTab('bookings');
dom.seatMapTab.onkeydown = handleTabKeydown;
dom.bookingsTab.onkeydown = handleTabKeydown;
dom.changeBuyerButton.onclick = () => {
  switchAppTab('map', {refresh: false});
  dom.buyerInput.focus();
};
dom.stadiumScroller.addEventListener('click', event => {
  const seatElement = event.target.closest('.seat');
  if (!seatElement || !dom.stadiumScroller.contains(seatElement)) return;
  handleSeatClick(seatElement.dataset.seatId, seatElement);
});
dom.bookingsList.addEventListener('click', event => {
  const button = event.target.closest('.cancel-booking-button');
  if (!button || !dom.bookingsList.contains(button)) return;
  const booking = bookingById.get(button.dataset.bookingId);
  if (booking) openCancelDialog(booking);
});
dom.cancelDialog.addEventListener('close', () => {
  if (!mutationInFlight) cancelTarget = null;
});
dom.cancelDialog.addEventListener('cancel', event => {
  if (mutationInFlight) event.preventDefault();
});
dom.buyerInput.addEventListener('input', () => {
  if (cancelTarget && !mutationInFlight) closeCancelDialog();
  abortBookingRefresh();
  // renderBookings computes the ownership diff, so changing buyers touches
  // only the previously-owned red seats instead of scanning all 400 chairs.
  renderBookings([]);
  clearTimeout(buyerRefreshTimer);
  buyerRefreshTimer = setTimeout(() => refreshBookings(), 350);
});

document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'visible') {
    refreshAndReschedule();
    if (hasPendingRefunds()) {
      refreshBookings({silent: true, force: true});
    } else {
      scheduleRefundStatusRefresh();
    }
  } else {
    clearTimeout(refreshTimer);
    clearTimeout(refundStatusTimer);
    refundStatusTimer = null;
  }
});

refreshAndReschedule();
refreshBookings({silent: true});
switchAppTab('map', {refresh: false});
