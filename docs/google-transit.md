# Publishing the feeds to Google Maps and others

GTFS-Realtime only works for a consumer that uses the **same static GTFS**
the feed refers to: TripUpdates name `trip_id`s, `stop_id`s and `route_id`s
from Rejseplanen's GTFS. Before submitting anywhere, confirm which static
feed the consumer uses for Denmark.

## Google Maps

Google accepts realtime data only from a transit agency (or a party acting
for it) through the Google Transit partner program. The steps below follow
Google's public partner documentation; check it for the current details.

1. **Owner of the static feed.** The realtime feed is attached to a static
   GTFS feed already in Google's Transit Partner Dashboard. If Google's
   Danish data comes from Rejseplanen's GTFS, its account owner submits the
   realtime feed. If Google gets data from individual operators instead, the
   trip_ids will not match and the bridge would need to run against that
   operator's GTFS (`-gtfs-url`).
2. **Host the feeds** at stable public HTTPS URLs, e.g.
   `https://gtfs-rt.example.dk/gtfs-rt/trip-updates`, `/vehicle-positions`
   and `/alerts`. Google fetches them periodically; aim for data no older
   than about 30–60 seconds (operator mode with `-journey-interval 30s`).
3. **Validate** with MobilityData's validator (CI does this on every push;
   see the Validation section of the README for running it on a live feed).
4. **Add the realtime feeds** in the Partner Dashboard for the static feed,
   and use its preview and quality reports before going live.

## Apple Maps

Apple works with transit agencies directly through Apple Maps Connect /
Apple's transit data partners. The same three feed URLs apply.

## Open journey planners

* **Transitous** (transitous.org) is a community-run planner that ingests
  GTFS and GTFS-RT; feeds are added by pull request to its feed list. Check
  its contribution rules for feeds not published by the agency.
* **Transitland** and the **Mobility Database** catalogue feeds so other
  apps can find them.
* Danish/EU national access point: under the EU ITS MMTIS regulation,
  realtime travel data is being phased into national access points. A public
  feed is a natural fit there.

## Checklist for an operator

- [ ] Static GTFS used by the consumer is Rejseplanen's (trip_ids match)
- [ ] Feeds hosted on HTTPS with monitoring (`/metrics`)
- [ ] `rpgtfsrt_journeys_by_outcome` shows a high match rate
- [ ] Validator: 0 errors on live snapshots
- [ ] Data licence and attribution agreed (Rejseplanen data, CC BY 4.0 for static)
