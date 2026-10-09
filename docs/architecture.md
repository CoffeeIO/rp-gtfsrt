# Architecture and data notes

## Pipeline

```
                    ┌──────────────┐
 GTFS.zip ────────► │ gtfs.Schedule│  compact: stop times as 16-byte values,
 (daily reload)     └──────┬───────┘  per-stop departure index, int32 ids
                           │
 ┌───────────┐  tasks ┌────▼──────┐  raw observations  ┌───────────┐
 │ scheduler │───────►│ Rejseplan │───────────────────►│ feed.Store│
 │ (pacer)   │        │ API client│                    └────┬──────┘
 └───────────┘        └───────────┘                         │ render (≤ every 5 s)
                                                            ▼
                                     matcher ──► TripUpdates / VehiclePositions
                                     AlertStore ──► Alerts
                                                            │
                                                  HTTP: protobuf, gzip, ETag
```

| Package | Role |
|---|---|
| `internal/gtfs` | Loads static GTFS into a compact schedule; finds trips by stop and minute. |
| `internal/rejseplanen` | API client and parsers for departureBoard, journeyDetail, himsearch. |
| `internal/matcher` | Maps API observations to GTFS trip instances. |
| `internal/feed` | Stores observations, renders the three feeds, serves HTTP. |
| `internal/scheduler` | Chooses the next API call; paces by budget or rate. |
| `internal/coverage` | Chooses discovery stations (greedy set cover). |
| `internal/simulator` | Fake Rejseplanen API generated from GTFS. |
| `internal/budget` | Monthly call budget with persistence. |
| `internal/metrics` | Prometheus text exposition. |

## Discovery and tracking

1. **Discovery.** Departure boards are polled at discovery stations. A trip is
   discovered when one of its first three stops is a discovery station.
   `plan-stops` computes the set: on 2026-10-09, 2,019 stations covered all
   44,319 trips; 180 stations already covered half of them.
2. **Tracking.** Each board row carries a journey reference. With
   `-journeys all`, every journey departing within `-journey-lookahead` is
   refreshed with journeyDetail every `-journey-interval`, giving all stops.
   With `-journeys cancelled` (the free-key default), only journeys with a
   cancelled departure are fetched, to tell partial from full cancellation.
3. **Priority.** The scheduler always runs the most overdue task. Cancelled
   journeys not yet fetched get a head start; failures back off
   exponentially (30 s … 10 min), unknown journeys for an hour.

## Matching

Observations are matched to a GTFS trip instance (trip_id + service date):

* **Journeys** (journeyDetail): candidates are trips calling at an anchor
  stop in the same minute; each candidate is aligned stop by stop and scored
  by stops whose scheduled time agrees. The best candidate must agree on at
  least two stops and beat the runner-up; a tie is broken only by the line
  name. This works for every mode because it does not depend on line names,
  which differ between the API and GTFS for many trains.
* **Board rows** (no journeyDetail yet): stop and minute, narrowed by line
  name when that helps, then by direction against the trip headsign.

Anything ambiguous is left out of the feed and counted in
`rpgtfsrt_journeys_by_outcome{outcome="ambiguous"}`. Publishing a delay on
the wrong trip is worse than publishing none.

Matching uses the GTFS service day (noon − 12 h), so trips past midnight
(`24:30:00`) and DST changes are handled. Stop ids are compared with leading
zeros stripped: the API says `8600626`, GTFS says `000008600626`. A station id
also matches its platforms (`parent_station`).

## Rejseplanen API behaviour (observed 2026-10-08/09)

* `prognosisType` (`PROGNOSED`/`CALCULATED`) is set on every tracked
  departure; `rtTime` is **omitted when the prediction equals the
  schedule**. Rows with a prognosis but no `rtTime` are published as delay 0
  (which also clears a delay once a train recovers). Rows without a
  prognosis carry no realtime data and are skipped.
* Cancelled departures: `"cancelled": true, "reachable": false`, no
  prognosis. journeyDetail marks stops with `cancelledArrival` /
  `cancelledDeparture` and whole journeys with `cancelled`.
* journeyDetail `lastPos` gives the vehicle position. `journeyPos` returned
  no data with a Labs key.
* Journey references encode the journey number (`ZI`) and day (`DA`); the
  bridge keys journeys by `ZI@DA`.
* `himsearch` returns at most **100 messages**; `maxNum` and `products` did
  not change that with a Labs key. Messages name affected stops and a
  product bitmask but **no lines**, so alerts are attached to stops only.
  Alerts whose stops are not in the GTFS are left out
  (`rpgtfsrt_alerts_unresolved`).
* The API has no vehicle ids, so `vehicle_id` is not populated
  (validator warning W002).

## Limitations

* **Refresh rate on a free key.** At ~20,000 calls a month the demo polls
  about once a minute (validator warning W007). An operator setup refreshes
  journeys every 30–60 s.
* **Alerts are capped** by the API at 100 and cannot be attached to lines.
* **Board-only matches** (before journeyDetail) are weaker; the national
  simulator runs had 1–2 % ambiguous journeys in the first minutes,
  before journeyDetail had been fetched for them.
* **Google needs matching trip_ids.** The feed references Rejseplanen GTFS
  trip_ids; a consumer must use the same static feed. See
  [google-transit.md](google-transit.md).
