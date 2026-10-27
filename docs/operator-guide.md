# Operator guide

How to run rp-gtfsrt for the whole of Denmark.

## Requirements

| | Free Labs key (demo) | Operator (whole country) |
|---|---|---|
| API access | Rejseplanen Labs key, ~20–50k calls/month | unthrottled access |
| CPU | 1 vCPU | 2 vCPU |
| Memory | 512 MB | 1 GB (peak ~430 MB at GTFS load, ~330 MB steady) |
| Disk | 200 MB (GTFS cache) | 200 MB |
| Outbound | rejseplanen.dk, rejseplanen.info | same |

## Run it

The image is `ghcr.io/coffeeio/rp-gtfsrt` (linux/amd64 and linux/arm64).

```sh
# 1. Choose discovery stations for the current timetable
docker run --rm -v rpdata:/data ghcr.io/coffeeio/rp-gtfsrt plan-stops -out /data/stops.txt

# 2. Serve all three feeds
docker run -d --name rp-gtfsrt -p 8080:8080 -v rpdata:/data \
  -e RP_ACCESS_ID=... -e RP_STOPS_FILE=/data/stops.txt \
  -e RP_MODE=rate -e RP_RATE=50 -e RP_CONCURRENCY=16 \
  -e RP_JOURNEYS=all -e RP_BOARD_INTERVAL=30m -e RP_JOURNEY_INTERVAL=1m \
  ghcr.io/coffeeio/rp-gtfsrt
```

Or `docker compose --profile operator up` with `RP_ACCESS_ID` in `.env`.

Re-run `plan-stops` when the timetable changes significantly (it is cheap:
a few seconds). The compose profile does it at every start.

### From SIRI-ET instead of the API

Rejseplanen also publishes its realtime data as SIRI-ET on the national
access point (Dataudveksleren), CC BY 4.0, with no call budget: one dataset
per operator (783, 785–790, 799–801, 808), delivered as Azure Service Bus
topic subscriptions over AMQP. To receive them:

1. Log in at Dataudveksleren with MitID Erhverv (a CVR number is needed).
2. Under Profil › Servicekonti, create a service account. Its password is
   shown once and cannot be recovered.
3. On each SIRI dataset, attach the service account (Tilknyt), then open
   Abonner på begivenhed data › Abonner for its tenant id and AMQP URL.

```sh
export RP_DU_TENANT_ID=... RP_DU_CLIENT_ID=... RP_DU_CLIENT_SECRET=...
export RP_SIRI_AMQP_URLS=amqps://<namespace>/t-distribution-785/<subscription>,...
rp-gtfsrt serve -source siri
```

Each message carries only the stops of one journey that changed; the bridge
merges the messages of a journey (newest wins) and matches the result to a
GTFS trip the same way as a journeyDetail response, by its stops and
scheduled times. SIRI journey ids are not GTFS trip_ids. Messages are
received once and deleted. With `-access-id`, the API is still used for
traffic alerts only. SIRI-ET has no vehicle positions, so that feed stays
empty.

Measured live on 2026-10-27 with all eleven subscriptions: after two
minutes 1,178 journeys, 1,168 matched, 4 ambiguous, 6 without a match;
trip updates for every operator including S-tog. Snapshots passed the
GTFS-RT validator with 0 errors (only W002, no vehicle ids).

`-siri-url` instead reads one SIRI-ET estimated timetable document over
HTTP, for example the public mirror
`http://gtfs.spline.de/siri/danmark/estimated-timetable.xml`, which carries
only some operators. On a saved national delivery (2026-10-25, 892
journeys) 848 matched, and every match agreed with the SIRI line or train
number.

### Try it without a key

`docker compose --profile simulator up` runs a simulated Rejseplanen API
generated from the real GTFS (with synthetic delays and cancellations) and
the bridge against it, at national scale.

## Endpoints

| Path | Content |
|---|---|
| `/gtfs-rt/trip-updates` | GTFS-RT TripUpdates (protobuf; `?format=json` for humans) |
| `/gtfs-rt/vehicle-positions` | GTFS-RT VehiclePositions |
| `/gtfs-rt/alerts` | GTFS-RT Alerts (Danish) |
| `/healthz` | JSON: match outcomes, feed sizes, API call counts, budget |
| `/metrics` | Prometheus metrics |

Feeds are full datasets, re-rendered at most every `-render-interval` (5 s),
gzip-encoded on request, with `ETag`/`If-None-Match` support.

## Configuration

Every flag can be set as an environment variable: `-journey-interval` →
`RP_JOURNEY_INTERVAL`.

| Flag | Default | Meaning |
|---|---|---|
| `-source` | `api` | realtime source: `api` or `siri` |
| `-siri-amqp-urls` | | siri source: Dataudveksleren AMQP subscription URLs |
| `-du-tenant-id`, `-du-client-id`, `-du-client-secret` | | siri AMQP: service account credentials |
| `-siri-url`, `-siri-interval` | , `1m` | siri source: SIRI-ET document URL and fetch interval (instead of AMQP) |
| `-access-id` | | Rejseplanen API key (required with `-source api`) |
| `-api-base` | `https://www.rejseplanen.dk/api` | API root |
| `-stops`, `-stops-file` | | discovery stations (from `plan-stops`) |
| `-mode` | `budget` | `budget` (monthly cap) or `rate` (calls/second) |
| `-rate` | `10` | rate mode: calls per second |
| `-monthly-budget` | `20000` | budget mode: calls per month (persisted in `data/budget.json`) |
| `-active-from`, `-active-to` | `5`, `1` | budget mode: daily polling hours |
| `-min-interval` | `15s` | budget mode: minimum gap between calls |
| `-concurrency` | `4` | parallel API calls |
| `-journeys` | `cancelled` | journeyDetail use: `off`, `cancelled`, `all` |
| `-journey-interval` | `1m` | refresh per journey |
| `-journey-lookahead` | `30m` | track journeys departing within this |
| `-board-interval` | `2m` | minimum time between polls of one station |
| `-window` | `60` | departure board window (minutes); keep ≥ 2 × board interval |
| `-max-journeys` | `-1` | departure board row limit (-1 = all) |
| `-products` | `0` | departure board product bitmask (16 = S-tog, 0 = all) |
| `-alerts-interval` | `15m` | traffic information refresh (0 = off) |
| `-routes`, `-route-types` | all | restrict the static GTFS (e.g. `-routes A,B,Bx,C,E,F,H`) |
| `-gtfs`, `-gtfs-url` | Rejseplanen Labs GTFS | static timetable source |
| `-gtfs-reload` | `24h` | reload interval; failures keep the previous timetable |
| `-render-interval` | `5s` | feed render cache |
| `-assume-on-time` | `false` | treat rows with no prognosis as on time |

## Sizing the API load

For the whole country (2026-10-09 timetable):

* **Discovery**: 2,019 stations. Polled every 30 minutes with a 60-minute
  window: ~1.1 calls/s.
* **Tracking**: one journeyDetail per active journey per interval. Up to a
  few thousand journeys run at once at peak; at a 60 s interval that is
  roughly 30–60 calls/s.
* **Alerts**: one call per interval.

`-rate 50 -concurrency 16` is a reasonable start; watch
`rpgtfsrt_api_calls_total` and the feed's freshness and adjust. A longer
`-journey-interval` trades freshness for load.

## Monitoring

Useful alerts on `/metrics`:

| Metric | Alert when |
|---|---|
| `rpgtfsrt_feed_timestamp_seconds` | older than 2 minutes (feed not rendering) |
| `rpgtfsrt_api_calls_total{status="error"}` | error share above a few percent |
| `rpgtfsrt_journeys_by_outcome{outcome="no_candidate"}` | jumps after a GTFS reload (timetable mismatch) |
| `rpgtfsrt_gtfs_loaded_timestamp_seconds` | older than 2 days (reloads failing) |
| `rpgtfsrt_gtfs_reload_failures_total` | increasing |
| `rpgtfsrt_feed_entities{feed="trip-updates"}` | near zero in daytime |

## Behaviour notes

* A GTFS reload swaps the timetable atomically; stored observations are
  re-matched on the next render, so trip_id changes in the weekly GTFS are
  handled.
* Journeys leave the feed 30 minutes after their scheduled end plus delay;
  unmatched observations are dropped after 2 hours.
* The process is stateless apart from the GTFS cache and, in budget mode,
  the monthly call counter. Running two instances behind a load balancer
  doubles API load; prefer one instance with a restart policy.
