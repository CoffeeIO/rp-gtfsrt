# Serving GTFS-Realtime natively

For Rejseplanen's developers. rp-gtfsrt builds GTFS-Realtime from outside,
by polling the public API and matching what it sees to the static GTFS.
Inside Rejseplanen's own systems most of that work falls away. This note
lists what stays, how each feed field maps to the data, and how to check a
native endpoint with the same tests the bridge uses.

## What falls away

| In the bridge | Natively |
|---|---|
| Polling, call budget, scheduler, discovery stations | Not needed: the realtime data is already there. |
| Matching each journey to a GTFS `trip_id` by stop order and times ([architecture.md](architecture.md#matching)) | Emit the `trip_id` the published GTFS.zip uses for that trip. |
| Journey references (`ZI@DA`) that go stale when the timetable version changes (seen as HTTP 400 `SVC_PARAM`) | Not needed. |
| Alerts capped at 100 and attached to stops only | All messages, and lines (`route_id`) as informed entities. |
| No `vehicle.id` (validator warning W002) | Fill it in if a vehicle id exists internally. |
| Refresh about once a minute on a free key (W007) | Render every 30 s or faster. |

The one hard requirement stays: **every `trip_id`, `route_id` and `stop_id`
must exist in the GTFS.zip that consumers have at that moment.** If the
static feed is regenerated daily, the ids must be stable across versions, or
the realtime feed must switch versions together with the static one.

## Endpoints

Three URLs, each returning a protobuf `FeedMessage`
(`application/x-protobuf`). The bridge serves them as below; using the same
paths lets you run its checks against your endpoint unchanged.

```
/gtfs-rt/trip-updates
/gtfs-rt/vehicle-positions
/gtfs-rt/alerts
```

Header for all three: `gtfs_realtime_version: "2.0"`,
`incrementality: FULL_DATASET`, `timestamp` = render time. The bridge also
gzips responses and sets an `ETag`; both are optional.

## Field mapping

The source column names the public API fields the bridge reads. Internally
the same values exist under other names.

### TripUpdates

| GTFS-RT field | Source | Notes |
|---|---|---|
| `entity.id` | Journey | Any id stable for the trip instance. |
| `trip.trip_id`, `trip.route_id` | GTFS | The bridge finds these by matching. |
| `trip.start_date` | GTFS service day | `YYYYMMDD` of the service day, so a `24:30:00` trip belongs to the day before. |
| `trip.schedule_relationship` | journeyDetail `cancelled` | `CANCELED` if the journey is cancelled or every stop is; otherwise `SCHEDULED`. |
| `trip_update.timestamp` | | When the prediction was made. |
| `stop_time_update.stop_sequence`, `stop_id` | GTFS `stop_times` | |
| `arrival.delay`, `arrival.time` | `rtArrTime`/`rtArrDate` minus `arrTime`/`arrDate` | Delay in seconds; time as Unix time. |
| `departure.delay`, `departure.time` | `rtDepTime`/`rtDepDate` minus `depTime`/`depDate` | |
| `stop_time_update.schedule_relationship` | `cancelledArrival`, `cancelledDeparture` | `SKIPPED` when the stop is cancelled; otherwise `SCHEDULED`, set explicitly (validator W009). |

Two API behaviours matter for the delay:

* `rtTime` is **omitted when the prediction equals the schedule**, while
  `prognosisType` is still set. The bridge publishes delay 0 then, which also
  clears an earlier delay once a train recovers.
* A stop with no prognosis at all carries no realtime data and is left out
  of the update.

### VehiclePositions

| GTFS-RT field | Source | Notes |
|---|---|---|
| `entity.id`, `trip` | As in TripUpdates | |
| `position.latitude`, `position.longitude` | journeyDetail `lastPos` | |
| `timestamp` | | When the position was observed. |
| `vehicle.id` | Not in the public API | Recommended if available. |

Cancelled trips get no position.

### Alerts

| GTFS-RT field | Source | Notes |
|---|---|---|
| `entity.id` | himsearch message `id` | |
| `header_text` | `head` | Language `da`. |
| `description_text` | `text`, else `lead` | Language `da`. |
| `active_period` | `sDate`/`sTime` to `eDate`/`eTime` | |
| `informed_entity` | `affectedStops` | GTFS `stop_id`s. Natively, add `route_id` for affected lines. |
| `cause`, `effect` | | `UNKNOWN_*`: the public API has no category. Map internal categories if they exist. |

Inactive (`act` false) and ended messages are left out.

Stop ids differ in form: the API says `8600626`, GTFS says `000008600626`.
A station id should also cover its platforms (`parent_station`).

## Checking a native endpoint

**Validator.** CI runs MobilityData's GTFS-Realtime validator on every push
and nightly on real S-tog data. To run it against your endpoint, save
snapshots for a while and point `scripts/validate.sh` at them:

```sh
# validator.jar: build it as in .github/workflows/conformance.yml
base=https://your-host/gtfs-rt
mkdir -p snap/TripUpdates snap/VehiclePositions snap/Alerts
cp GTFS.zip snap/gtfs.zip            # the static feed the snapshots refer to
for i in $(seq 20); do
  ts=$(date +%Y%m%dT%H%M%S)
  curl -sf -o snap/TripUpdates/$ts.pb      $base/trip-updates
  curl -sf -o snap/VehiclePositions/$ts.pb $base/vehicle-positions
  curl -sf -o snap/Alerts/$ts.pb           $base/alerts
  sleep 30
done
scripts/validate.sh validator.jar snap
```

It fails on any ERROR-level finding and prints the warnings.

**Side by side.** Run the bridge on the same day (see
[operator-guide.md](operator-guide.md)). Both feeds should name the same
`trip_id`s; delays should differ only by refresh timing.

**Google.** Submitting the feeds is described in
[google-transit.md](google-transit.md).

## Starting from SIRI-ET

Rejseplanen's SIRI-ET streams on the national access point already carry
the delays and cancellations. `rp-gtfsrt serve -source siri` converts a
SIRI-ET document to GTFS-Realtime ([operator-guide.md](operator-guide.md#from-siri-et-instead-of-the-api)),
so the conversion can run on Rejseplanen's own data without the API. A
native version would add the GTFS `trip_id` to each journey and skip the
matching.

## Where to look in the code

| File | What it shows |
|---|---|
| `internal/feed/render.go` | Building TripUpdates and VehiclePositions from journeys and board rows. |
| `internal/siri/` | Reading SIRI-ET into the same journey model. |
| `internal/feed/alerts.go` | Building Alerts and resolving stop ids. |
| `internal/rejseplanen/` | Which API fields are read, with real responses in `testdata/`. |
| `internal/matcher/` | Trip matching; only needed from outside. |
| `scripts/validate.sh` | The validator run used in CI. |
