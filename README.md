<p align="center">
  <img src="internal/web/static/javbeacon-banner-dark.png" alt="JAVBeacon — See what's coming. Get what you want." width="100%">
</p>

<p align="center">
  A private, self-hosted release monitor and acquisition manager for Japanese adult video collections.
</p>

## What is JAVBeacon?

JAVBeacon watches configured JAV release sources, builds a searchable release library, reconciles releases with your local StashApp collection, finds matching torrents, and manages downloads through qBittorrent.

It is designed as a private, single-user application. The recommended Docker Compose stack stores application data in PostgreSQL; standalone installs can still use SQLite.

> **See what's coming. Get what you want.**

See [CHANGELOG.md](CHANGELOG.md) for the complete version history.

## Highlights

- Monitor GIGA/Akiba and JavLibrary sources for new and updated releases.
- Browse a responsive cover library with structured filters for actresses, studios, labels, tags, dates, duration, and release state.
- Track upcoming, desired, local, downloading, completed, failed, and missing releases.
- Search Sukebei/Nyaa-compatible providers and submit vetted torrents to qBittorrent.
- Run scheduled searches for recent and older monitored releases, with independent intervals and filename acceptance rules.
- Use per-site RSS monitoring, duplicate prevention, torrent progress, seed/peer status, and download history.
- Reconcile releases with StashApp, synchronize Desired tags, and scan the Stash library for missing files.
- Run ordered post-download and post-removal pipelines, including path mapping, shell commands, moves, and StashApp scans.
- Cache cover artwork locally and receive live release, job, download, notification, and server-log updates.
- Run the complete Docker Compose stack with JAVBeacon, optimized PostgreSQL, and Byparr, or use SQLite for a simpler standalone deployment.

## Quick start with Docker

### Requirements

- Docker 24 or newer with Docker Compose
- A private host or trusted network
- Enough memory for the selected PostgreSQL profile; the supplied Large Library profile assumes roughly 8 GB is available to PostgreSQL
- Optional integrations: qBittorrent and StashApp

The supplied Compose stack starts three services:

- **JAVBeacon** — the web application
- **PostgreSQL 18** — persistent storage with the same Large Library / SSD optimizations offered by the built-in Compose generator
- **Byparr** — the required anti-bot solver for JavLibrary scraping

Clone the repository and create your private environment file:

```bash
git clone https://github.com/Net005/JAVBeacon.git
cd JAVBeacon
cp .env.example .env
```

Edit `.env` and replace both `JAVBEACON_INITIAL_PASSWORD` and
`POSTGRES_PASSWORD` before the first start. Review the PostgreSQL memory values
if the host cannot dedicate roughly 8 GB to PostgreSQL. The `.env` file is
intentionally ignored by Git so credentials are not committed.

The Compose defaults already point JAVBeacon at PostgreSQL through
`postgres:5432` and Byparr through `http://byparr:8191/v1`; do not replace
those service hostnames with `localhost`. Validate and start the stack:

```bash
docker compose config
docker compose pull
docker compose up -d
docker compose ps
```

Wait for all three services to report healthy. Byparr's local health endpoint
and API documentation are available only from the Docker host by default:

```bash
curl http://127.0.0.1:8191/health
docker compose logs byparr
```

Open [http://localhost:8080](http://localhost:8080), sign in, then configure
providers and integrations under **Settings**. PostgreSQL data is stored under
`POSTGRES_DATA_PATH` (default `./data/postgres`). JAVBeacon's cover cache and
other application files are stored under `JAVBEACON_DATA_PATH` (the provided
`.env.example` uses `./data/javbeacon`). Both paths may be relative to the
Compose project or absolute host paths, for example:

```dotenv
JAVBEACON_DATA_PATH=/srv/javbeacon/data
POSTGRES_DATA_PATH=/srv/javbeacon/postgres
```

If `JAVBEACON_DATA_PATH` is omitted, Compose continues to use the existing
`javbeacon-data` named volume for backward compatibility.

`JAVBEACON_LISTEN` and `JAVBEACON_FLARESOLVERR_URL` are also configurable in
`.env`. Cover files use `/app/data/covers` by default and are persisted by the
configured data mount; their location can be changed later under **Settings →
Storage**. The included Byparr service uses `http://byparr:8191/v1`; when using
an external service instead, provide its plain reachable URL with the `/v1`
path and no Markdown formatting.

Compose tracks the latest published JAVBeacon image. Upgrade it without
changing `.env`:

```bash
docker compose pull
docker compose up -d
```

### GitHub Container package

Each version tag publishes a public, multi-architecture container package for
Linux AMD64 and ARM64. Compose uses `latest` by default; versioned images can
also be pulled directly for deployments that deliberately require a fixed
release:

```bash
docker pull ghcr.io/net005/javbeacon:1.0.8
docker pull ghcr.io/net005/javbeacon:latest
```

Published tags include `v1.0.8`, `1.0.8`, `1.0`, and `latest`. See the
[JAVBeacon GitHub package](https://github.com/Net005/JAVBeacon/pkgs/container/javbeacon)
for available versions and digests. To build the application image locally
instead, keep the repository checkout and run `docker compose up -d --build`.

### Standalone Docker with SQLite

The full Compose stack is recommended. If you deliberately run JAVBeacon with
SQLite instead, Byparr must still share a Docker network with it for
JavLibrary scraping:

```bash
docker network create javbeacon
docker run -d \
  --name javbeacon-byparr \
  --network javbeacon \
  --restart unless-stopped \
  -p 127.0.0.1:8191:8191 \
  --shm-size 512m \
  ghcr.io/thephaseless/byparr:latest

docker build --build-arg VERSION=1.0.8 -t javbeacon:1.0.8 .
docker volume create javbeacon-data

docker run -d \
  --name javbeacon \
  --network javbeacon \
  --restart unless-stopped \
  -p 8080:8080 \
  -v javbeacon-data:/app/data \
  -e JAVBEACON_INITIAL_USERNAME=admin \
  -e JAVBEACON_INITIAL_PASSWORD='replace-with-a-long-password' \
  -e JAVBEACON_FLARESOLVERR_URL='http://javbeacon-byparr:8191/v1' \
  javbeacon:1.0.8
```

If initial credentials are not supplied, JAVBeacon creates `admin` / `changeme123`. Change them immediately.

## Run from source

### Requirements

- Go 1.25 or newer
- Git

```bash
git clone https://github.com/Net005/JAVBeacon.git
cd JAVBeacon
go run ./cmd/javbeacon
```

JAVBeacon listens on [http://localhost:8080](http://localhost:8080) and stores its SQLite database at `data/javbeacon.db` by default.

JavLibrary scraping also requires Byparr when running from source. Start the
official image and point JAVBeacon at the host-published endpoint:

```bash
docker run -d \
  --name javbeacon-byparr \
  --restart unless-stopped \
  -p 127.0.0.1:8191:8191 \
  --shm-size 512m \
  ghcr.io/thephaseless/byparr:latest

export JAVBEACON_FLARESOLVERR_URL=http://127.0.0.1:8191/v1
go run ./cmd/javbeacon
```

Byparr listens on port `8191`; `/v1` is its FlareSolverr-compatible request
endpoint. See the [official Byparr documentation](https://github.com/ThePhaseless/Byparr#readme) for proxy, locale, and troubleshooting options. Challenge bypass is best-effort and can still depend on the host's network reputation.

Build a standalone binary with:

```bash
go build -o javbeacon ./cmd/javbeacon
./javbeacon
```

Print the running binary's version with `./javbeacon -version`. The same
version is shown at the bottom of the web sidebar near **Sign out**.

## First-time setup

1. Sign in and replace the default credentials.
2. Confirm **Settings → Scraping → Byparr / FlareSolverr** shows `http://byparr:8191/v1` for Compose, or the reachable `/v1` endpoint you configured for a standalone install. Byparr (or a compatible FlareSolverr service) is required for JavLibrary scraping.
3. Add monitoring sources under **Sites** and choose whether each source should notify, mark releases as Desired, or automate searches. JavLibrary URLs must include `&mode=2` to include future releases.
4. Open **Settings → Downloads** and configure the search URL template, accepted filename patterns, and qBittorrent connection.
5. Optionally configure **Settings → StashApp** for local-library synchronization, Desired-tag synchronization, missing-file scans, and path remapping.
6. Review automation schedules before enabling unattended scraping or downloading.

## Configuration

Most configuration is stored in the active database and managed from the web interface. The following environment variables control bootstrap and database connectivity:

| Variable | Purpose | Default |
| --- | --- | --- |
| `JAVBEACON_LISTEN` | HTTP listen address | `:8080` |
| `JAVBEACON_DB` | SQLite database path | `data/javbeacon.db` |
| `JAVBEACON_COVERS` | Initial cover-cache directory | `data/covers` |
| `JAVBEACON_INITIAL_USERNAME` | Username created on the first start | `admin` |
| `JAVBEACON_INITIAL_PASSWORD` | Password created on the first start | `changeme123` |
| `JAVBEACON_API_KEY` | Optional API compatibility key | unset |
| `JAVBEACON_FLARESOLVERR_URL` | Initial Byparr or FlareSolverr-compatible `/v1` endpoint (legacy variable name) | `http://127.0.0.1:8191/v1`; Compose uses `http://byparr:8191/v1` |
| `JAVBEACON_PAGE_LIMIT` | Initial scrape page limit | `5` |
| `JAVBEACON_DB_ENGINE` | `sqlite` or `postgres` | `sqlite` |
| `JAVBEACON_DB_HOST` | PostgreSQL host | `127.0.0.1` |
| `JAVBEACON_DB_PORT` | PostgreSQL port | `5432` |
| `JAVBEACON_DB_NAME` | PostgreSQL database | `javbeacon` |
| `JAVBEACON_DB_USER` | PostgreSQL user | `javbeacon` |
| `JAVBEACON_DB_PASSWORD` | PostgreSQL password | unset |
| `JAVBEACON_DB_SSLMODE` | PostgreSQL SSL mode | `prefer` |

Settings, credentials, sessions, schedules, release state, download history, notifications, and pipelines are persisted in the selected database. Back it up before upgrades or migrations.

## PostgreSQL

The official Compose stack uses optimized PostgreSQL 18 by default. A standalone JAVBeacon process still defaults to SQLite. For an existing SQLite installation, open **Settings → Database** to generate a tailored PostgreSQL stack or migrate with validation.

To start directly against PostgreSQL, provide the required environment variables before launching JAVBeacon:

```bash
export JAVBEACON_DB_ENGINE=postgres
export JAVBEACON_DB_HOST=127.0.0.1
export JAVBEACON_DB_PORT=5432
export JAVBEACON_DB_NAME=javbeacon
export JAVBEACON_DB_USER=javbeacon
export JAVBEACON_DB_PASSWORD='replace-with-a-strong-password'
export JAVBEACON_DB_SSLMODE=prefer

go run ./cmd/javbeacon
```

JAVBeacon does not silently fall back to SQLite when PostgreSQL is configured. If the database is unavailable, it starts a recovery interface and keeps retrying the configured connection.

## Credential recovery

Stop the running application and use either or both reset flags:

```bash
go run ./cmd/javbeacon --reset-username my-user
go run ./cmd/javbeacon --reset-password 'a-new-long-password'
```

For a Docker deployment, run the same binary inside the container with the persistent data volume attached.

## Development

```bash
go test ./...
go build -o javbeacon ./cmd/javbeacon
```

The web client is embedded from `internal/web/static` into the Go binary.

### Versioning and releases

JAVBeacon uses semantic versions. `internal/version/VERSION` is the source of
truth used by local builds, Docker image metadata, the version API, and the
frontend. Release tags use the same value with a `v` prefix—for example,
`VERSION=1.0.8` is released as `v1.0.8`.

Pushing a matching `v*` tag runs the GitHub release workflow. It executes the
test suite, validates the Compose stack, builds Linux, macOS, and Windows
binaries, creates checksums, publishes a multi-architecture GHCR container
package with provenance, and publishes the release as the latest GitHub
release. A tag that does not match the checked-in version is rejected.

Every release must also have a dated `## [x.y.z] - YYYY-MM-DD` entry in
`CHANGELOG.md`. Add changes to its `Unreleased` section as work lands; during a
version bump, rename those notes to the release version and date and create a
new empty `Unreleased` section. The release workflow rejects tags without a
matching changelog entry. Run `go generate -mod=mod ./internal/version` after
editing the changelog so packaged binaries include the same release notes; CI
rejects a stale embedded copy.

### Project layout

- `cmd/javbeacon` — application entry point
- `internal/web` — HTTP API, WebSocket updates, and embedded web client
- `internal/monitor` — scrape scheduling and job orchestration
- `internal/scraper` — GIGA/Akiba and JavLibrary providers
- `internal/download` — search, RSS, qBittorrent, notifications, and pipelines
- `internal/stash` — StashApp synchronization and missing-file recovery
- `internal/store` — SQLite/PostgreSQL persistence and migrations
- `internal/auth` — single-user authentication and sessions

## Security and responsible use

JAVBeacon is intended for private, authenticated use. Do not expose it directly to the public internet. Put it behind a trusted reverse proxy with TLS, use a strong password, restrict network access, and protect the data directory because it contains credentials and application state.

You are responsible for complying with the laws, site terms, and content rights applicable in your jurisdiction.

<p align="center">
  <img src="internal/web/static/javbeacon-site-logo-dark.png" alt="JAVBeacon emblem" width="128">
</p>
