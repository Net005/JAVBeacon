<p align="center">
  <img src="internal/web/static/javbeacon-banner-dark.png" alt="JAVBeacon — See what's coming. Get what you want." width="100%">
</p>

<p align="center">
  A private, self-hosted release monitor and acquisition manager for Japanese adult video collections.
</p>

## What is JAVBeacon?

JAVBeacon watches configured JAV release sources, builds a searchable release library, reconciles releases with your local StashApp collection, finds matching torrents, and manages downloads through qBittorrent.

It is designed as a private, single-user application. Metadata, credentials, automation settings, download history, pipeline state, and notifications are stored in SQLite by default, with PostgreSQL available for larger deployments.

> **See what's coming. Get what you want.**

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
- Use SQLite for a simple deployment or migrate to PostgreSQL through the built-in setup and migration wizards.

## Quick start with Docker

### Requirements

- Docker 24 or newer
- A private host or trusted network
- Optional: qBittorrent, StashApp, and FlareSolverr

Clone the repository and create your private environment file:

```bash
git clone https://github.com/Net005/JAVBeacon.git
cd JAVBeacon
cp .env.example .env
```

Edit `.env` and replace `JAVBEACON_INITIAL_PASSWORD` before the first start.
The `.env` file is intentionally ignored by Git so credentials are not
committed. Then build and start the Compose stack:

```bash
docker compose up -d --build
docker compose ps
```

Open [http://localhost:8080](http://localhost:8080), sign in, then configure providers and integrations under **Settings**. The named `javbeacon-data` volume keeps the database and cover cache across upgrades.

To upgrade to a newer checked-out release, update `JAVBEACON_VERSION` in
`.env`, pull the matching Git tag, and rebuild:

```bash
git fetch --tags
git checkout v1.0.0
docker compose up -d --build
```

### Docker without Compose

The equivalent direct Docker workflow is:

```bash
docker build --build-arg VERSION=1.0.0 -t javbeacon:1.0.0 .
docker volume create javbeacon-data

docker run -d \
  --name javbeacon \
  --restart unless-stopped \
  -p 8080:8080 \
  -v javbeacon-data:/app/data \
  -e JAVBEACON_INITIAL_USERNAME=admin \
  -e JAVBEACON_INITIAL_PASSWORD='replace-with-a-long-password' \
  javbeacon:1.0.0
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

Build a standalone binary with:

```bash
go build -o javbeacon ./cmd/javbeacon
./javbeacon
```

Print the running binary's version with `./javbeacon -version`. The same
version is shown at the bottom of the web sidebar near **Sign out**.

## First-time setup

1. Sign in and replace the default credentials.
2. Open **Settings → Scraping** and configure FlareSolverr if a source requires Cloudflare challenge handling.
3. Add monitoring sources under **Sites** and choose whether each source should notify, mark releases as Desired, or automate searches.
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
| `JAVBEACON_FLARESOLVERR_URL` | Initial FlareSolverr endpoint | application default |
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

SQLite is the simplest option and remains the default. For larger libraries, open **Settings → Database** to generate a PostgreSQL stack or migrate an existing SQLite database with validation.

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
`VERSION=1.0.0` is released as `v1.0.0`.

Pushing a matching `v*` tag runs the GitHub release workflow. It executes the
test suite, builds Linux, macOS, and Windows binaries, creates checksums, and
publishes the release as the latest GitHub release. A tag that does not match
the checked-in version is rejected.

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
