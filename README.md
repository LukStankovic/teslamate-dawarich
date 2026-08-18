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

### If Dawarich is on a public domain

`DAWARICH_URL: https://dawarich.example.com` works as well — the image ships CA
certificates and a trailing slash is trimmed. An internal address is still
preferable:

- Routers that do not support NAT hairpinning cannot reach your own public IP
  from inside the LAN.
- A backfill sends hundreds of requests through whatever proxy or CDN sits in
  front, where a WAF, a rate limit or a request timeout can cut it short.
- Any authentication layer in front of Dawarich returns a login page instead of
  JSON, which the client treats as a permanent failure and does not retry.

To keep one URL where hairpinning fails, resolve the domain to the internal
address for this container only. TLS still validates, because the hostname is
unchanged:

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

```sh
# Everything TeslaMate ever recorded, then exit.
docker compose run --rm teslamate-dawarich -full -once

# From a specific date.
docker compose run --rm teslamate-dawarich -from 2024-01-01 -once
```

Both rewrite the stored cursor, so the daemon carries on from the end of the
backfill afterwards. Re-running a backfill is harmless.

Already imported some drives as GPX? Delete that import in Dawarich before
backfilling. Deleting an import removes its points, which avoids two slightly
different copies of the same drive — GPX rounds coordinates and timestamps, so
those points would not deduplicate against these ones.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `DAWARICH_URL` | — | Base URL of your Dawarich instance |
| `DAWARICH_API_KEY` | — | Dawarich API key |
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

## License

MIT
