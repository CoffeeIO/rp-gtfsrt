# rp-gtfsrt

[![ci](https://github.com/CoffeeIO/rp-gtfsrt/actions/workflows/ci.yml/badge.svg)](https://github.com/CoffeeIO/rp-gtfsrt/actions/workflows/ci.yml)
[![conformance](https://github.com/CoffeeIO/rp-gtfsrt/actions/workflows/conformance.yml/badge.svg)](https://github.com/CoffeeIO/rp-gtfsrt/actions/workflows/conformance.yml)
[![real-gtfs](https://github.com/CoffeeIO/rp-gtfsrt/actions/workflows/realfeed.yml/badge.svg)](https://github.com/CoffeeIO/rp-gtfsrt/actions/workflows/realfeed.yml)

Open-source bridge that turns Rejseplanen's realtime data into
**GTFS-Realtime** (TripUpdates, VehiclePositions and Alerts) for every
train, metro, light rail and bus in Denmark, so Google Maps, Apple Maps and
other journey planners can show real departure times, delays and
cancellations.

> 🇩🇰 [Kort resumé på dansk](docs/resume-da.md)

## Why

Google Maps in Denmark shows scheduled times only. Google gets Danish
timetables as static GTFS, but realtime needs a separate GTFS-Realtime feed,
and none is published. The data exists: Rejseplanen, DSB and the regional
apps all show it. This project is that missing feed, ready for an operator
to run.

## What it does

```
Rejseplanen GTFS (static) ─► compact national schedule (4.2M stop times, ~425 MB)
                                         │
Rejseplanen API ─ departureBoard ─┐      │
                ─ journeyDetail  ─┼─► store ─► matcher ─► /gtfs-rt/trip-updates
                ─ himsearch      ─┘                      /gtfs-rt/vehicle-positions
                                                         /gtfs-rt/alerts
```

* **Every stop of every trip.** Departure boards at a computed set of
  discovery stations find journeys; journeyDetail gives each journey's full
  stop list with live arrival and departure times.
* **Matched to the static GTFS by stop sequence and time**, not by line
  names, so it works across all modes. Ambiguous matches are dropped rather
  than guessed.
* **Cancellations**: whole-trip `CANCELED`, or `SKIPPED` stops for partial
  cancellations.
* **Vehicle positions** from the API's last known position.
* **Service alerts** from Rejseplanen traffic information, attached to the
  affected stops (Danish text).
* **Safe GTFS reloads**: observations are stored raw and re-matched when the
  weekly timetable (and its trip_ids) changes.
* **Two pacing modes**: `budget` keeps a free Labs key under its monthly
  quota; `rate` runs at a fixed calls/second for an operator.
* **Operable**: Docker image, Compose profiles, Prometheus `/metrics`,
  `/healthz`, gzip and ETag on feeds.

## Results (2026-10-08/09)

| Check | Result |
|---|---|
| Every trip in Denmark on one day (44,319), all modes, through the journey matcher | 44,276 matched, 43 ambiguous (duplicate trips in the GTFS), **0 wrong** |
| Live API, S-tog hubs, 10 minutes | 1,345 / 1,345 departures matched |
| Live API with journeyDetail | 33 / 33 journeys matched, full-trip updates with per-stop delays |
| Live cancellations | both cancelled Bx trains matched their GTFS trips |
| MobilityData GTFS-Realtime validator | **0 errors** (live snapshots, CI fixture, nightly real S-tog) |
| National load test against the simulator | 2,019 stations, 4,800 journey calls in 3 min, 0 errors, render 0.1 s, 327 MB |

## Quick start

```sh
# No API key: simulated Rejseplanen API, whole country
docker compose --profile simulator up

# Free Labs key: S-tog demo within the monthly budget
echo RP_ACCESS_ID=your-key > .env
docker compose --profile demo up

# Operator: every trip in Denmark
docker compose --profile operator up
```

Then open `http://localhost:8080/gtfs-rt/trip-updates?format=json`.

Without Docker:

```sh
go run ./cmd/rp-gtfsrt plan-stops -out stops.txt          # discovery stations
RP_ACCESS_ID=... go run ./cmd/rp-gtfsrt serve -stops-file stops.txt -mode rate -rate 50 -journeys all
```

## Documentation

* [Operator guide](docs/operator-guide.md): deployment, configuration, sizing, monitoring
* [Publishing to Google Maps and others](docs/google-transit.md)
* [Architecture and data notes](docs/architecture.md): matching, API behaviour, limitations

## Validation

The feeds are checked with MobilityData's
[GTFS-Realtime validator](https://github.com/MobilityData/gtfs-realtime-validator):
on every push against the test fixture, nightly against the real S-tog
timetable (`.github/workflows/conformance.yml`), and on live snapshots.
Result: **0 errors**. Remaining warnings:

| Rule | Why |
|---|---|
| W002 `vehicle_id` not populated | The Rejseplanen API exposes no vehicle id. |
| W007 refresh interval > 35 s | The free-key budget allows about one poll per minute; operator mode refreshes faster. |

To validate a running instance, save a few snapshots per feed a minute apart
into one directory per feed type, put the GTFS next to them as `gtfs.zip`,
and run `scripts/validate.sh validator.jar <dir>` (needs Java 17 and jq).

## Development

```sh
make test       # unit, end-to-end and simulator tests, no network
make race       # what CI runs
make realfeed   # all-modes matcher round-trip on the real GTFS (no API calls)
make live       # one real API call (RP_ACCESS_ID, RP_LIVE_STOP)
```

## Data and licence

Timetable data © Rejseplanen, CC BY 4.0. Realtime data from the Rejseplanen
Labs API under its terms. Code is Apache-2.0.
