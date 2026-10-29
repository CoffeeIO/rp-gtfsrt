// Replays a recorded day of rp-gtfsrt feeds (data/<source>/day.json, written
// by cmd/demo-export). Everything is computed from the recorded GTFS-Realtime
// state of each trip plus its scheduled stop times.
"use strict";

const SKIPPED = -(1 << 20);
const LINE_COLORS = { A: "#2a7ac0", B: "#4d9a2a", Bx: "#8cc63f", C: "#e07b00", E: "#7a4fa3", F: "#f2b600", H: "#c8323a" };
const TZ = "Europe/Copenhagen";
const fmtTime = new Intl.DateTimeFormat("en-GB", { hour: "2-digit", minute: "2-digit", hourCycle: "h23", timeZone: TZ });
const fmtDay = new Intl.DateTimeFormat("en-GB", { weekday: "long", day: "numeric", month: "long", year: "numeric", timeZone: TZ });

// The same day recorded from two sources; #src= picks one.
const SOURCES = {
  siri: {
    dir: "data/siri/",
    how: "The bridge subscribed to Rejseplanen's SIRI-ET realtime streams on Dataudveksleren, the Danish national access point, and saved its three feeds once a minute. This page replays those files; nothing here calls Rejseplanen.",
    scope: "The recording covers every operator in Denmark; this page shows the S-tog part. SIRI needs no API calls, so nothing limits how much is followed.",
  },
  api: {
    dir: "data/api/",
    how: "The bridge polled Rejseplanen's API with a free Labs key (about one call every 20 seconds, København H and Nørreport boards plus journey details) and saved its three feeds once a minute. This page replays those files; nothing here calls Rejseplanen.",
    scope: "A free key allows a few hundred calls an hour, so only trips seen on those two boards are followed. The SIRI source covers every trip.",
  },
};
const el = (id) => document.getElementById(id);
let data, map, trainLayer, now, playing = false, speed = 30, lastTick = 0;
const markers = new Map();
const requested = (location.hash.match(/src=(\w+)/) || [])[1];
const source = requested in SOURCES ? requested : "siri";

fetch(SOURCES[source].dir + "day.json").then((response) => response.json()).then(start).catch((error) => {
  document.querySelector(".grid").innerHTML = `<p class="card">Could not load the recording: ${error}</p>`;
});

function start(json) {
  data = json;
  const first = data.frames[0][0], last = data.frames[data.frames.length - 1][0];
  const slider = el("slider");
  slider.min = first; slider.max = last;
  el("day").textContent = fmtDay.format(new Date(first * 1000));

  const stationNames = [...new Set([...data.stations, ...data.stops.map((stop) => stop[0])])];
  el("station").innerHTML = stationNames.map((name) => `<option>${escapeHtml(name)}</option>`).join("");
  el("station").addEventListener("change", render);

  setupMap();
  drawTimeline();
  daySummary();
  downloads();

  const fromHash = parseHash();
  const morning = first + secondsUntil(first, 8, 0);
  now = fromHash ?? (morning > first && morning < last ? morning : first);
  slider.value = now;
  slider.addEventListener("input", () => { now = +slider.value; render(); });
  el("play").addEventListener("click", togglePlay);
  for (const button of document.querySelectorAll(".speed button")) {
    button.addEventListener("click", () => {
      speed = +button.dataset.speed;
      document.querySelectorAll(".speed button").forEach((other) => other.classList.toggle("on", other === button));
    });
  }
  // Switching source reloads the page at the same time of day.
  for (const button of document.querySelectorAll(".source button")) {
    button.classList.toggle("on", button.dataset.source === source);
    button.setAttribute("aria-pressed", button.dataset.source === source);
    button.addEventListener("click", () => {
      if (button.dataset.source === source) return;
      location.hash = `src=${button.dataset.source}&t=${fmtTime.format(new Date(now * 1000)).replace(":", "")}`;
      location.reload();
    });
  }
  el("about-how").textContent = SOURCES[source].how;
  el("about-scope").textContent = SOURCES[source].scope;
  render();
}

// trip.st holds [unix, cancelled, delay per stop...]; the page uses
// the latest state at or before the replay time.
function stateAt(trip, t) {
  const states = trip.st;
  if (!states.length || states[0][0] > t || t > trip.l + 120) return null;
  if (inGap(t)) return null;
  let lo = 0, hi = states.length - 1;
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1;
    if (states[mid][0] <= t) lo = mid; else hi = mid - 1;
  }
  return states[lo];
}

function delayAt(state, index) {
  for (let i = index; i >= 0; i--) {
    const delay = state[2 + i];
    if (delay !== SKIPPED) return delay;
  }
  return 0;
}

function liveTimes(trip, state, index) {
  const delay = delayAt(state, index);
  const [, arrival, departure] = trip.s[index];
  return { arrival: trip.b + arrival + delay, departure: trip.b + departure + delay, delay };
}

function position(trip, state, t) {
  const n = trip.s.length;
  if (liveTimes(trip, state, 0).departure > t || liveTimes(trip, state, n - 1).arrival < t) return null;
  for (let i = 0; i < n - 1; i++) {
    const here = liveTimes(trip, state, i), next = liveTimes(trip, state, i + 1);
    if (t < here.departure) return { latlng: stopLatLng(trip.s[i][0]), delay: here.delay, at: i };
    if (t < next.arrival) {
      const share = (t - here.departure) / Math.max(1, next.arrival - here.departure);
      const [aLat, aLon] = stopLatLng(trip.s[i][0]), [bLat, bLon] = stopLatLng(trip.s[i + 1][0]);
      return { latlng: [aLat + (bLat - aLat) * share, aLon + (bLon - aLon) * share], delay: next.delay, at: i + 1 };
    }
  }
  return { latlng: stopLatLng(trip.s[n - 1][0]), delay: delayAt(state, n - 1), at: n - 1 };
}

const stopLatLng = (index) => [data.stops[index][1], data.stops[index][2]];

function render() {
  el("clock").textContent = fmtTime.format(new Date(now * 1000));
  el("slider").value = now;
  const gap = gaps().find(([from, to]) => now > from + 60 && now < to);
  el("gap").hidden = !gap;
  if (gap) el("gap").textContent = `No recording ${fmtTime.format(new Date(gap[0] * 1000))}–${fmtTime.format(new Date(gap[1] * 1000))}: trains are missing here, not cancelled.`;
  renderBoard();
  renderMap();
  renderStats();
  renderAlerts();
  moveCursor();
}

function renderBoard() {
  const station = el("station").value;
  const rows = [];
  for (const trip of data.trips) {
    for (let i = 0; i < trip.s.length - 1; i++) {
      if (data.stops[trip.s[i][0]][0] !== station) continue;
      const planned = trip.b + trip.s[i][2];
      const state = stateAt(trip, now);
      const live = state && !state[1] ? liveTimes(trip, state, i) : null;
      const shown = live ? live.departure : planned;
      if (shown < now - 30 || planned > now + 3600) continue;
      const skipped = state && state[2 + i] === SKIPPED;
      rows.push({ trip, planned, live, cancelled: Boolean(state && (state[1] || skipped)), known: Boolean(state), sort: shown });
    }
  }
  rows.sort((a, b) => a.sort - b.sort);
  const visible = rows.slice(0, 12);
  el("board-empty").hidden = visible.length > 0;
  el("board").innerHTML = visible.map((row) => {
    const live = row.cancelled
      ? `<span class="very-t">Cancelled</span>`
      : row.live
        ? `${fmtTime.format(new Date(row.live.departure * 1000))}<small class="${delayClass(row.live.delay)}-t">${delayLabel(row.live.delay)}</small>`
        : `<span class="muted">not yet in feed</span>`;
    return `<tr class="${row.cancelled ? "cancelled" : ""}">
      <td>${lineBadge(row.trip.r)}</td>
      <td class="to" title="${escapeHtml(row.trip.h)}">${escapeHtml(row.trip.h)}</td>
      <td class="num planned">${fmtTime.format(new Date(row.planned * 1000))}</td>
      <td class="num live">${live}</td>
    </tr>`;
  }).join("");
}

function setupMap() {
  map = L.map("map", { zoomControl: true, attributionControl: true, scrollWheelZoom: false });
  L.tileLayer("https://tile.openstreetmap.org/{z}/{x}/{y}.png", {
    attribution: "© <a href=\"https://www.openstreetmap.org/copyright\">OpenStreetMap</a> contributors",
    maxZoom: 18, className: "tiles",
  }).addTo(map);
  const bounds = [];
  for (const [route, stops] of Object.entries(data.routes)) {
    const line = stops.map(stopLatLng);
    bounds.push(...line);
    L.polyline(line, { color: LINE_COLORS[route] || "#888", weight: 4, opacity: 0.55 }).addTo(map);
  }
  const drawn = new Set();
  for (const stops of Object.values(data.routes)) {
    for (const index of stops) {
      const name = data.stops[index][0];
      if (drawn.has(name)) continue;
      drawn.add(name);
      L.circleMarker(stopLatLng(index), { radius: 3, weight: 1, color: "#777", fillColor: "#fff", fillOpacity: 1 })
        .bindTooltip(name).on("click", () => { el("station").value = name; render(); })
        .addTo(map);
    }
  }
  if (bounds.length) map.fitBounds(bounds, { padding: [10, 10] });
  else map.setView([55.68, 12.57], 11);
  trainLayer = L.layerGroup().addTo(map);
}

function renderMap() {
  const seen = new Set();
  for (const trip of data.trips) {
    const state = stateAt(trip, now);
    if (!state || state[1]) continue;
    const where = position(trip, state, now);
    if (!where) continue;
    seen.add(trip);
    const color = cssVar(`--${delayClass(where.delay)}`);
    let marker = markers.get(trip);
    if (!marker) {
      marker = L.circleMarker(where.latlng, { radius: 7, weight: 3, fillOpacity: 1 });
      marker.bindTooltip("", { className: "train-tip" });
      markers.set(trip, marker);
      trainLayer.addLayer(marker);
    }
    marker.setLatLng(where.latlng);
    marker.setStyle({ color, fillColor: LINE_COLORS[trip.r] || "#888" });
    marker.setTooltipContent(`<strong>${escapeHtml(trip.r)}</strong> to ${escapeHtml(trip.h)}<br>${delayLabel(where.delay)}`);
  }
  for (const [trip, marker] of markers) {
    if (!seen.has(trip)) { trainLayer.removeLayer(marker); markers.delete(trip); }
  }
}

function renderStats() {
  let inFeed = 0, late = 0, max = 0, cancelled = 0;
  for (const trip of data.trips) {
    const state = stateAt(trip, now);
    if (!state) continue;
    inFeed++;
    if (state[1]) { cancelled++; continue; }
    const where = position(trip, state, now);
    if (!where) continue;
    if (where.delay >= 120) late++;
    max = Math.max(max, where.delay);
  }
  el("s-trips").textContent = inFeed;
  el("s-late").textContent = late;
  el("s-max").textContent = max >= 60 ? `${Math.round(max / 60)} min` : "on time";
  el("s-cancel").textContent = cancelled;
}

function renderAlerts() {
  const names = new Set(data.stops.map((stop) => stop[0]));
  const active = (data.alerts || []).filter((alert) => alert.f <= now && now <= alert.t + 60 && alert.s?.some((name) => names.has(name)));
  el("alerts").innerHTML = active.length
    ? `<h3>Traffic information right now</h3>` + active.slice(0, 4).map((alert) =>
        `<div class="alert">${escapeHtml(alert.h || alert.d)}<small>${escapeHtml(alert.s.slice(0, 4).join(", "))}${alert.s.length > 4 ? " …" : ""}</small></div>`).join("")
    : "";
}

// Per-minute count of trains running 2+ minutes late, across the day.
let timelineScale;
function drawTimeline() {
  const svg = el("timeline");
  const first = data.frames[0][0], last = data.frames[data.frames.length - 1][0];
  const step = 300, buckets = [];
  for (let t = first; t <= last; t += step) {
    let late = 0;
    for (const trip of data.trips) {
      const state = stateAt(trip, t);
      if (!state || state[1]) continue;
      const where = position(trip, state, t);
      if (where && where.delay >= 120) late++;
    }
    buckets.push([t, late]);
  }
  const peak = Math.max(1, ...buckets.map((bucket) => bucket[1]));
  const width = 600, height = 74, barWidth = width / buckets.length;
  timelineScale = (t) => ((t - first) / Math.max(1, last - first)) * width;
  let svgContent = gaps().map(([from, to]) =>
    `<rect class="gap" x="${timelineScale(from).toFixed(1)}" y="0" width="${Math.max(1, timelineScale(to) - timelineScale(from)).toFixed(1)}" height="${height}"><title>No data ${fmtTime.format(new Date(from * 1000))}–${fmtTime.format(new Date(to * 1000))}</title></rect>`).join("") + buckets.map(([t, late], i) =>
    `<rect class="bar" x="${(i * barWidth).toFixed(1)}" y="${(height - (late / peak) * height).toFixed(1)}" width="${Math.max(1, barWidth - 0.5).toFixed(1)}" height="${((late / peak) * height).toFixed(1)}"><title>${fmtTime.format(new Date(t * 1000))}: ${late} delayed</title></rect>`).join("");
  for (let hour = 0; hour < 24; hour += 3) {
    const t = first + secondsUntil(first, hour, 0);
    if (t > first && t < last) svgContent += `<text class="axis" x="${timelineScale(t).toFixed(1)}" y="88" text-anchor="middle">${String(hour).padStart(2, "0")}</text>`;
  }
  svgContent += `<line class="cursor" id="cursor" x1="0" x2="0" y1="0" y2="${height}"></line>`;
  svg.innerHTML = svgContent;
  svg.addEventListener("click", (event) => {
    const box = svg.getBoundingClientRect();
    now = Math.round(first + ((event.clientX - box.left) / box.width) * (last - first));
    render();
  });
}

const inGap = (t) => gaps().some(([from, to]) => t > from + 120 && t < to);

// Stretches of more than three minutes without a snapshot (the recording
// machine was asleep or offline).
let gapCache;
function gaps() {
  if (gapCache) return gapCache;
  const out = [];
  for (let i = 1; i < data.frames.length; i++) {
    if (data.frames[i][0] - data.frames[i - 1][0] > 180) out.push([data.frames[i - 1][0], data.frames[i][0]]);
  }
  return (gapCache = out);
}

function moveCursor() {
  const x = timelineScale(now).toFixed(1);
  el("cursor").setAttribute("x1", x);
  el("cursor").setAttribute("x2", x);
}

// The largest delay a trip showed for at least two minutes, so a prediction
// that jumps for a single snapshot does not count. States are stored only on
// change; each lasts until the next one or until the trip leaves the feed.
function heldDelay(trip, lastFrame) {
  const states = trip.st;
  const worst = states.map((state) => {
    if (state[1]) return 0;
    let w = 0;
    for (let i = 2; i < state.length; i++) if (state[i] !== SKIPPED) w = Math.max(w, state[i]);
    return w;
  });
  const end = (j) => j + 1 < states.length ? states[j + 1][0] : Math.min(trip.l + 120, lastFrame);
  let best = 0;
  for (let i = 0; i < states.length; i++) {
    let held = worst[i];
    for (let j = i; j < states.length; j++) {
      held = Math.min(held, worst[j]);
      if (end(j) - states[i][0] >= 120) { best = Math.max(best, held); break; }
    }
  }
  return best;
}

function daySummary() {
  const lastFrame = data.frames[data.frames.length - 1][0];
  let tracked = 0, everLate = 0, cancelled = 0, maxDelay = 0, maxTrip = null;
  for (const trip of data.trips) {
    if (!trip.st.length) continue;
    tracked++;
    const worst = heldDelay(trip, lastFrame);
    if (trip.st.some((state) => state[1])) cancelled++;
    if (worst >= 180) everLate++;
    if (worst > maxDelay) { maxDelay = worst; maxTrip = trip; }
  }
  const calls = data.frames[data.frames.length - 1][5] - data.frames[0][5];
  el("day-stats").innerHTML = [
    ["Trips with live data", tracked.toLocaleString("en")],
    ["Delayed 3+ min (held 2+ min)", `${everLate} <small class="muted">(${tracked ? Math.round((everLate / tracked) * 100) : 0}%)</small>`],
    ["Cancelled", cancelled],
    ["Largest delay", maxDelay >= 60 ? `${Math.round(maxDelay / 60)} min <small class="muted">${maxTrip ? maxTrip.r : ""}</small>` : "–"],
    ["Snapshots recorded", data.frames.length.toLocaleString("en")],
    source === "siri" ? ["API calls used", "none"] : ["API calls used", calls > 0 ? calls.toLocaleString("en") : "–"],
  ].map(([label, value]) => `<div><dt>${label}</dt><dd>${value}</dd></div>`).join("");
}

function downloads() {
  const samples = data.samples || {};
  const links = ["trip-updates", "vehicle-positions", "alerts"].filter((feed) => samples[feed])
    .map((feed) => `<a href="${SOURCES[source].dir}${samples[feed]}" download>${feed}.pb</a>`);
  el("downloads").innerHTML = links.length
    ? `Raw GTFS-Realtime protobuf from ${samples.at ? fmtTime.format(parseStamp(samples.at)) : "the recording"}: ${links.join(" · ")}`
    : "";
}

function togglePlay() {
  playing = !playing;
  el("play").textContent = playing ? "❚❚" : "▶";
  el("play").setAttribute("aria-label", playing ? "Pause" : "Play");
  lastTick = performance.now();
  if (playing) requestAnimationFrame(tick);
}

function tick(stamp) {
  if (!playing) return;
  const elapsed = (stamp - lastTick) / 1000;
  if (elapsed >= 0.25) {
    lastTick = stamp;
    now = Math.min(+el("slider").max, now + elapsed * speed);
    render();
    if (now >= +el("slider").max) togglePlay();
  }
  requestAnimationFrame(tick);
}

function delayClass(delay) { return delay >= 300 ? "very" : delay >= 120 ? "late" : "ok"; }
function delayLabel(delay) {
  if (delay >= 60) return `+${Math.round(delay / 60)} min`;
  if (delay <= -60) return `${Math.round(delay / 60)} min`;
  return "on time";
}
function lineBadge(route) { return `<span class="line" style="background:${LINE_COLORS[route] || "#888"}">${escapeHtml(route)}</span>`; }
function cssVar(name) { return getComputedStyle(document.documentElement).getPropertyValue(name).trim(); }
function escapeHtml(text) { return String(text).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]); }

// Seconds from the day of `t` local midnight to hh:mm, relative to t.
function secondsUntil(t, hours, minutes) {
  const parts = Object.fromEntries(new Intl.DateTimeFormat("en-GB", { hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23", timeZone: TZ })
    .formatToParts(new Date(t * 1000)).map((part) => [part.type, +part.value]));
  return (hours - parts.hour) * 3600 + (minutes - parts.minute) * 60 - parts.second;
}
function parseHash() {
  const match = location.hash.match(/t=(\d{2})(\d{2})/);
  if (!match || !data) return null;
  return data.frames[0][0] + secondsUntil(data.frames[0][0], +match[1], +match[2]);
}
function parseStamp(stamp) {
  const t = data.frames[0][0] + secondsUntil(data.frames[0][0], +stamp.slice(9, 11), +stamp.slice(11, 13));
  return new Date(t * 1000);
}
