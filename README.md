# TeslaMate Dawarich

Streams your Tesla's positions from [TeslaMate](https://github.com/teslamate-org/teslamate)
into [Dawarich](https://github.com/Freika/dawarich), live, and backfills your
whole driving history in one command.

A single Go binary. No queue, no broker requirement, no state beyond one small
cursor file.

## How it works

TeslaMate already records every position it gets from the car in its Postgres
`positions` table, with an accurate timestamp. This daemon reads new rows from
there and posts them to Dawarich's Overland batch endpoint.

Reading from Postgres instead of MQTT matters:

- **Real timestamps.** MQTT publishes latitude and longitude on separate topics
  with no timestamp, so an MQTT-only importer has to guess when a point was
  recorded and pair two topics by hand.
- **No gaps.** If the daemon, the network or Dawarich is down for an hour, the
  rows are still in Postgres and the next pass picks them up. Nothing is lost
  and no local queue is needed.
- **Backfill is the same code path.** Point the cursor at 2019 and the live
  syncer becomes a history importer.

Setting `MQTT_HOST` subscribes to `teslamate/cars/+/latitude` and uses it purely
as a **nudge**: a published position triggers an immediate poll instead of
waiting for the next tick. Latency drops to about a second while the data still
comes from Postgres.

### Exactly-once without the machinery

The cursor is the timestamp of the newest position already sent, and every pass
re-reads a five-minute window before it. TeslaMate assigns `positions.id` before
the transaction commits, so a strictly increasing id cursor can silently skip a
row that becomes visible late; a time window with overlap cannot.

Re-sending costs nothing because Dawarich upserts points on
`(longitude, latitude, timestamp, user)`. Duplicates are impossible, so retries,
overlaps and repeated backfills are all safe. The cursor advances only after
Dawarich has accepted a batch, so a failure mid-sync resumes instead of skipping.

## Setup

The daemon needs two things: TeslaMate's Postgres and Dawarich's HTTP port. The
easiest place to get both is **TeslaMate's own `docker-compose.yml`**, because
adding the service there puts it on the same Compose network as `database` and
`mosquitto` — both then resolve by name, exactly as they do for TeslaMate itself.

Add this service to your TeslaMate `docker-compose.yml`:

```yaml
services:
  teslamate-dawarich:
    image: lukstankovic/teslamate-dawarich:latest
    restart: unless-stopped
    depends_on:
      - database
    environment:
      DAWARICH_URL: http://dawarich_app:3000
      DAWARICH_API_KEY: your_dawarich_api_key
      # Same credentials TeslaMate uses; a read-only role works too.
      TESLAMATE_DB_URL: postgres://teslamate:${DATABASE_PASS}@database:5432/teslamate
      # Optional: sync the moment a position is published instead of polling.
      MQTT_HOST: mosquitto
      LOG_LEVEL: info
    volumes:
      # Holds the sync cursor. Without it, a restart re-syncs INITIAL_LOOKBACK.
      - teslamate-dawarich-data:/data

volumes:
  teslamate-dawarich-data:
```

Keep it a named volume. The container runs as a non-root user, and a named
volume inherits `/data`'s ownership from the image; a bind mount to a host
directory does not, so the cursor could not be written.

Wire it to Dawarich as described below, then start it:

```sh
docker compose up -d teslamate-dawarich
```

### Reaching Dawarich

TeslaMate and Dawarich run in separate Compose projects, so they are on separate
networks. Pick one of the two options below.

#### Join Dawarich's network

Dawarich's compose file declares a network called `dawarich`, which Compose
prefixes with its project name. Find the real name:

```sh
docker network ls | grep dawarich
```

Then add it to the service and declare it as external, keeping
`DAWARICH_URL: http://dawarich_app:3000` from above:

```yaml
services:
  teslamate-dawarich:
    # ... the service from above, plus:
    networks:
      - default          # keeps database and mosquitto reachable
      - dawarich

networks:
  dawarich:
    external: true
    name: dawarich_dawarich   # whatever `docker network ls` printed
```

#### Or use the published port

Skip the network plumbing and point at the host, using its LAN address rather
than `localhost` — inside a container that means the container itself:

```yaml
services:
  teslamate-dawarich:
    environment:
      DAWARICH_URL: http://192.168.1.10:3000
```

Dawarich and TeslaMate's Grafana both default to host port 3000, so on a shared
host one of them is published elsewhere. Use the port Dawarich actually listens
on.

### If Dawarich forces HTTPS

Setting `APPLICATION_PROTOCOL=https` turns on Rails' `force_ssl`, so Dawarich
answers plain HTTP with a redirect. The daemon refuses to follow redirects — a
redirected POST would silently become a GET — and tells you what to do instead:

- point `DAWARICH_URL` at the HTTPS address, or
- keep the internal HTTP address and set `DAWARICH_FORWARDED_PROTO=https`, the
  same trick Dawarich's own health check uses.

Dawarich also authorises the `Host` header against `APPLICATION_HOSTS`. Whatever
name you put in `DAWARICH_URL` must be listed there, so reaching a container by
its service name means adding `dawarich_app` to that list. An address that is
already allowed — the LAN IP or the public domain — needs no change.

A public URL works too, but an internal address avoids two traps: routers that
cannot reach your own public IP from inside the LAN, and the proxy or CDN in
front of a backfill's hundreds of requests. To keep one URL where hairpinning
fails, resolve the domain to the internal address for this container only. TLS
still validates, because the hostname is unchanged:

```yaml
services:
  teslamate-dawarich:
    environment:
      DAWARICH_URL: https://dawarich.example.com
    extra_hosts:
      - "dawarich.example.com:192.168.1.10"
```

### Where the API key lives

In Dawarich: user menu → **Settings** → API key (the same key `/api-docs`
refers to). Generating a new one there invalidates the old one, so update the
environment if you regenerate it.

## Backfilling history

Stop the running service first: both containers share `/data`, and they would
overwrite each other's cursor.

```sh
docker compose stop teslamate-dawarich
docker compose run --rm teslamate-dawarich -full -once      # or -from 2024-01-01
docker compose up -d teslamate-dawarich
```

`-full` and `-from` set the time range only. Which positions are synced stays
with `DRIVES_ONLY`.

Both rewrite the stored cursor, so the daemon carries on from the end of the
backfill afterwards. Re-running a backfill is harmless.

Already imported some drives as GPX? Delete that import in Dawarich before
backfilling. Deleting an import removes its points, which avoids two slightly
different copies of the same drive — GPX rounds coordinates and timestamps, so
those points would not deduplicate against these ones.

## Checking that it works

```sh
docker compose logs --tail 50 teslamate-dawarich
```

A pass that sent something logs `sync pass complete points=N`. Silence is not a
failure: with the defaults the first pass only covers the last 24 hours of
drives, so a day without driving has nothing to send. To see every query, run
one pass verbosely:

```sh
docker compose run --rm -e LOG_LEVEL=debug teslamate-dawarich -once
```

The cursor shows how far the sync has got:

```sh
docker compose exec teslamate-dawarich cat /data/bookmark.json
```

Dawarich itself is the final word. Points carry the car name as `tracker_id`,
which distinguishes them from a phone or an earlier GPX import:

```sh
curl -s "https://dawarich.example.com/api/v1/points?api_key=KEY&start_at=2026-08-01&end_at=2026-08-19&per_page=5&order=desc"
```

`start_at` and `end_at` accept an ISO date or a Unix timestamp, and the response
headers carry `X-Total-Pages`. On the map, a drive appears as a track once
Sidekiq has processed the new points, a few seconds behind the insert.

To confirm a backfill covered everything, compare its `points=N` with what
TeslaMate holds:

```sh
docker compose exec -T database psql -U teslamate -d teslamate -c \
"SELECT count(*), min(date), max(date) FROM positions
 WHERE latitude IS NOT NULL AND longitude IS NOT NULL AND drive_id IS NOT NULL;"
```

`N` runs a few percent above that count, because each page re-reads the overlap
window and Dawarich discards what it already has. `N` below the count means
something did not arrive.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `DAWARICH_URL` | — | Base URL of your Dawarich instance |
| `DAWARICH_API_KEY` | — | Dawarich API key |
| `DAWARICH_FORWARDED_PROTO` | — | Set to `https` when reaching a `force_ssl` instance over internal HTTP |
| `TESLAMATE_DB_URL` | — | TeslaMate Postgres connection string |
| `POLL_INTERVAL` | `15s` | Upper bound on sync latency |
| `OVERLAP_WINDOW` | `5m` | How far behind the cursor each pass re-reads |
| `INITIAL_LOOKBACK` | `24h` | Where the first run starts with no cursor |
| `BATCH_SIZE` | `1000` | Points per Dawarich request |
| `DRIVES_ONLY` | `true` | `false` also syncs parked and charging positions |
| `CAR_IDS` | all | Comma-separated TeslaMate car ids |
| `TRACKER_PREFIX` | — | Prepended to the car name to form Dawarich's `tracker_id` |
| `MQTT_HOST` | — | Set to enable nudging; unset disables MQTT entirely |
| `MQTT_PORT` | `1883` | `8883` when `MQTT_TLS=true` and no port is given |
| `MQTT_USERNAME` / `MQTT_PASSWORD` | — | Broker credentials |
| `MQTT_CLIENT_ID` | `teslamate-dawarich` | Broker client id |
| `MQTT_TLS` | `false` | TLS to the broker |
| `MQTT_NAMESPACE` | — | TeslaMate's `MQTT_NAMESPACE`, if you set one |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

`DRIVES_ONLY=true` is the default because a parked car keeps producing near
identical positions, which bloats Dawarich and skews its visit detection.

## Flags

| Flag | Effect |
| --- | --- |
| `-once` | Sync everything pending, then exit (useful from cron) |
| `-from <time>` | Move the cursor to an RFC3339 timestamp or `YYYY-MM-DD` |
| `-full` | Move the cursor to the beginning of time |
| `-version` | Print the version |

## What ends up in Dawarich

| Dawarich | TeslaMate |
| --- | --- |
| coordinates | `latitude`, `longitude` |
| `timestamp` | `date` |
| `altitude` | `elevation` |
| `velocity` | `speed`, converted from km/h to m/s |
| `battery` | `battery_level` |
| `battery_status` | `charging` when `power` is negative, else `unplugged` |
| `tracker_id` | `TRACKER_PREFIX` + car name |

Images are published to Docker Hub as `lukstankovic/teslamate-dawarich` and to
`ghcr.io/lukstankovic/teslamate-dawarich`.

## Development

```sh
make test   # go test -race ./...
make lint   # golangci-lint
make build
```

## Releasing

Tagging is the trigger. `make version` prints the next patch, minor and major
version; `make release-patch`, `-minor` or `-major` runs the tests, tags, pushes,
writes the GitHub release and appends its notes to `CHANGELOG.md`. The tag starts
the release workflow, which builds the multi-arch image and pushes it to Docker
Hub and ghcr — around six minutes, most of it arm64 under emulation.

The workflow needs `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN` as repository
secrets. The Docker Hub repository does not need to exist beforehand; the first
push creates it.

## License

MIT
