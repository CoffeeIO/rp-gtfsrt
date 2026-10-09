#!/usr/bin/env bash
# Runs MobilityData's GTFS-Realtime validator over feed snapshots written by
# TestWriteConformanceSnapshots and fails on any ERROR-level finding.
#
#   scripts/validate.sh <validator.jar> <snapshot dir>
#
# The snapshot dir holds gtfs.zip and one directory per feed type.
set -euo pipefail
jar=$1
dir=$2
status=0
for kind in TripUpdates VehiclePositions Alerts; do
  java -jar "$jar" -gtfs "$dir/gtfs.zip" -gtfsRealtimePath "$dir/$kind" -sort name > "$dir/$kind.log" 2>&1
  echo "== $kind"
  findings=$(cat "$dir/$kind"/*.results.json | jq -r '.[] | "\(.errorMessage.validationRule.severity) \(.errorMessage.validationRule.errorId) \(.errorMessage.validationRule.title)"' | sort | uniq -c)
  echo "${findings:-  no findings}"
  if grep -q ' ERROR ' <<<"$findings"; then
    status=1
  fi
done
exit $status
