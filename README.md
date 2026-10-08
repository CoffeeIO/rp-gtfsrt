# rp-gtfsrt

[![ci](https://github.com/CoffeeIO/rp-gtfsrt/actions/workflows/ci.yml/badge.svg)](https://github.com/CoffeeIO/rp-gtfsrt/actions/workflows/ci.yml)
[![real-gtfs](https://github.com/CoffeeIO/rp-gtfsrt/actions/workflows/realfeed.yml/badge.svg)](https://github.com/CoffeeIO/rp-gtfsrt/actions/workflows/realfeed.yml)

An open-source bridge that turns Rejseplanen's live departure data into a
**GTFS-Realtime TripUpdates** feed, so that apps like Google Maps, Apple Maps,
Transit and Transitous can show real Danish departure times and delays.

## Why

Google Maps in Denmark shows scheduled times only. Google gets Danish
timetables as static GTFS (from Rejseplanen), but realtime needs a separate
GTFS-Realtime feed sent by the operator, and none is published. The delay data
exists: Rejseplanen, DSB and the regional apps all show it.

This project is that missing feed, built in the open, for an operator to run.

## How it works

```
Rejseplanen GTFS.zip (weekly) ──► schedule index: (stop, line, minute, service day) → trip_id
Rejseplanen API departureBoard ──► departures with time / rtTime / cancelled
                                   │
                         matcher ──┘  picks exactly one GTFS trip per departure,
                                      disambiguating by direction; ambiguous → dropped
                                   │
                           store ──► /gtfs-rt/trip-updates   (protobuf, GTFS-RT 2.0)
```

* Delays are published as `StopTimeUpdate`s at the polled hub stops.
  GTFS-RT consumers propagate a delay to the stops after it.
* Cancelled departures become `CANCELED` trips.
* Trips past midnight (`24:30:00`) and DST changes follow the GTFS
  service day (noon − 12h).
* The live API sets `prognosisType` (`PROGNOSED`/`CALCULATED`) on every
  tracked departure and omits `rtTime` when it equals the schedule. Such rows
  are published as delay 0, which also clears a delay once a train recovers.
  Rows without `prognosisType` carry no realtime data and are skipped
  (`-assume-on-time` publishes them as on time instead).

## Demo scope and the API budget

The free Rejseplanen Labs key is limited (reportedly 50,000 calls/month). That
cannot cover the whole country, so the demo polls a few **S-tog hub stations**
and paces itself to stay within `-monthly-budget`, spread over the service
hours (`-active-from` / `-active-to`). Usage is persisted in `data/budget.json`.

Run by Rejseplanen or an operator with unrestricted access, the same code
covers every line: list more hubs, or feed it from their internal systems.

## Usage

```sh
# find hub stop ids in the static feed
go run ./cmd/rp-gtfsrt stops "København H"

# serve the feed
RP_ACCESS_ID=your-key go run ./cmd/rp-gtfsrt serve \
  -hubs 8600626,8600646      # København H, Nørreport

curl localhost:8080/gtfs-rt/trip-updates?format=json   # readable
curl localhost:8080/gtfs-rt/trip-updates               # protobuf
curl localhost:8080/healthz                            # stats + budget
```

Every flag can also be set as an `RP_*` environment variable (`-access-id` →
`RP_ACCESS_ID`, `-hubs` → `RP_HUBS`, ...). Run `rp-gtfsrt serve -h` for all of them.

**Stop ids.** The API uses zero-padded ids (e.g. `000052887`). Matching
ignores leading zeros and also tries `stop_code` and `parent_station`. Where
the API and GTFS ids still differ, map them with `-stop-aliases`.

### Docker

```sh
docker build -t rp-gtfsrt .
docker run -p 8080:8080 -v $PWD/data:/data -e RP_ACCESS_ID=... -e RP_HUBS=... rp-gtfsrt
```

## Tests

```sh
make test     # unit + end-to-end, no network
make race     # with the race detector (what CI runs)
make cover
RP_ACCESS_ID=... RP_LIVE_STOP=8600626 make live   # one real API call
make realfeed                       # real GTFS, no API: replay a day through the matcher
```

The suite uses a small hand-written S-tog GTFS fixture
(`internal/gtfs/gtfstest`) that covers platform/parent stops, calendar
exceptions, after-midnight trips, opposite-direction trips at the same minute
and a filtered bus route. `internal/e2e` runs the whole pipeline against a fake
API and decodes the served protobuf.

## Validation

Live feed snapshots were checked with MobilityData's
[GTFS-Realtime validator](https://github.com/MobilityData/gtfs-realtime-validator)
(batch mode, against an S-tog-only subset of the Rejseplanen GTFS) on 2026-10-08:
**0 errors**. Remaining warnings:

| Rule | Why |
|---|---|
| W002 `vehicle_id` not populated | The Rejseplanen API exposes no vehicle id. |
| W007 refresh interval > 35 s | Free-key API budget allows about one poll per minute. Goes away with operator access. |

```sh
go run ./cmd/gtfs-subset gtfs.zip stog.zip 109     # S-tog only (route_type 109)
# validator lib jar built from source (needs Java 17)
java -jar gtfs-realtime-validator-lib-1.0.0-SNAPSHOT-withAllDependencies.jar \
  -gtfs stog.zip -gtfsRealtimePath snapshots/ -sort name
```

## Status / open questions

- [x] Live API JSON shape confirmed 2026-10-08 (captured in
      `internal/rejseplanen/testdata/departureboard_live_20261008.json`).
- [x] API stop ids: `stopExtId` is `8600626`, GTFS has `000008600626`;
      leading-zero normalization matches them without aliases.
- [x] Matcher round-trip on real GTFS: 1782/1782 S-tog hub departures on 2026-10-08
- [x] Live match rate, 10 min at København H + Nørreport: 1345/1345 departures matched, 0 errors.
- [x] Cancellations confirmed live (`"cancelled": true`, no `prognosisType`); both
      cancelled Bx trains in a 3-hour board matched their GTFS trips.
- [x] GTFS-RT validator: 0 errors on live snapshots (see Validation).
- [ ] Ask Rejseplanen whether their HAFAS backend can already export GTFS-RT.

## Data and licence

Timetable data © Rejseplanen, CC BY 4.0. Live data is from the Rejseplanen
Labs API under its terms. Code is Apache-2.0.
