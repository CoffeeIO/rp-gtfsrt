.PHONY: test race cover live realfeed build docker

test:
	go test ./...

race:
	go test -race -count=1 ./...

cover:
	go test -coverprofile=cover.out ./... && go tool cover -func=cover.out | tail -1

# Spends one real API call. Needs RP_ACCESS_ID and RP_LIVE_STOP.
live:
	go test ./internal/rejseplanen -run Live -v -count=1

build:
	go build -o bin/rp-gtfsrt ./cmd/rp-gtfsrt

docker:
	docker build -t rp-gtfsrt .

# Replays a day's scheduled hub departures through the matcher. No API calls.
realfeed:
	RP_GTFS_ZIP=$${RP_GTFS_ZIP:-data/gtfs.zip} RP_DATE=$${RP_DATE:-$$(date +%F)} go test ./internal/matcher -run Real -v -count=1 -timeout 20m
