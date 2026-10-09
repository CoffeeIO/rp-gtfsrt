FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /rp-gtfsrt ./cmd/rp-gtfsrt \
 && mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /rp-gtfsrt /rp-gtfsrt
COPY --from=build --chown=nonroot:nonroot /data /data
ENV RP_CACHE_DIR=/data
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/rp-gtfsrt"]
CMD ["serve"]
