<p align="center">
  <img src="internal/web/static/javbeacon-banner-dark.png" alt="JAVBeacon — See what's coming. Get what you want." width="100%">
</p>

<p align="center">
  A private, self-hosted release monitor and acquisition manager for Japanese adult video collections.
</p>

## What is JAVBeacon?

JAVBeacon watches configured JAV release sources, builds a searchable release
library, reconciles releases with your local StashApp collection, and finds
matching downloads through qBittorrent or direct HTTP sources.

It is designed as a private, single-user application. The recommended Docker Compose stack stores application data in PostgreSQL; standalone installs can still use SQLite.

> **See what's coming. Get what you want.**

See [CHANGELOG.md](CHANGELOG.md) for the complete version history.

## Highlights

- Monitor GIGA/Akiba and JavLibrary sources for new and updated releases.
- Browse a responsive cover library with structured filters for actresses, studios, labels, tags, dates, duration, and release state.
- Track upcoming, watchlist, local, downloading, completed, failed, and missing releases.
- Search Sukebei/Nyaa-compatible providers and JavDB/Keepshare together, then
  download through qBittorrent or the built-in direct HTTP downloader.
- Run scheduled searches for recent and older monitored releases, with
  independent intervals and preferred filename rules shared by Torrent and
  HTTP candidate ranking.
- Use per-site RSS monitoring, duplicate prevention, Torrent seed/peer status,
  HTTP progress and ETA, automatic provider fallback, and download history.
- Reconcile releases with StashApp, synchronize Watchlist tags, and scan the Stash library for missing files.
- Run ordered post-download and post-removal pipelines, including path mapping, shell commands, moves, and StashApp scans.
- Cache cover artwork and JavLibrary screenshots locally, preview screenshots
  as card slideshows, and browse them from Release Details.
- Scale the full interface from the in-app user settings and receive live
  release, job, download, notification, and server-log updates.
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
PUID=1000
PGID=1000
```

If `JAVBEACON_DATA_PATH` is omitted, Compose continues to use the existing
`javbeacon-data` named volume for backward compatibility.

On every container start, JAVBeacon applies `PUID` and `PGID` (both default to
`1000`) to the application-data mount, including existing covers and
screenshots, then runs as that identity. The legacy `GUID` spelling is accepted
as a fallback for `PGID`. These variables affect `JAVBEACON_DATA_PATH`; the
PostgreSQL cluster remains owned and managed by the PostgreSQL container.

`JAVBEACON_LISTEN`, `JAVBEACON_SCREENSHOTS`, and
`JAVBEACON_FLARESOLVERR_URL` are also configurable in `.env`. Cover files use
`/app/data/covers`; screenshot previews use the distinct
`/app/data/screenshots` cache. Both are persisted by the configured data mount.
Historical screenshot maintenance is available under **Settings → Storage**;
it works newest-first at a low priority and remembers completed releases so it
can be safely resumed. The included Byparr service uses
`http://byparr:8191/v1`; when using an external service instead, provide its
plain reachable URL with the `/v1` path and no Markdown formatting.

The **Jobs** page also provides a manual-only **JavLibrary historical
backfill**. It discovers date-sorted genre, performer, and maker indexes,
deduplicates releases across that graph, skips IDs already present anywhere in
the JAVBeacon release library, and stores every source/page/item checkpoint in
the application database. Leave **Resume** selected after a
restart: JAVBeacon replays each index from its newest page until it relocates
the saved release-date boundary, captures anything inserted while the job was
offline, and then continues deeper. Clearing Resume resets only the backfill
discovery/checkpoint cache; releases already in the library are not deleted.
The job uses one JavLibrary request at a time, is never scheduled, and defaults
to priority `500`, allowing ordinary scraping work to use the shared solver
pool first. The Jobs page reports durable all-time source/page/release totals
separately from current-run counts; page totals are estimates until every
index has exposed its last page.

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
docker pull ghcr.io/net005/javbeacon:1.0.27
docker pull ghcr.io/net005/javbeacon:latest
```

Published tags include `v1.0.27`, `1.0.27`, `1.0`, and `latest`. See the
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

docker build --build-arg VERSION=1.0.27 -t javbeacon:1.0.27 .
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
  -e TZ='UTC' \
  javbeacon:1.0.27
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
3. Add monitoring sources under **Sites** and choose whether each source should notify, add releases to the Watchlist, or automate searches. JavLibrary URLs must include `&mode=2` to include future releases.
4. Open **Settings → Downloads** and configure the Torrent search URL,
   preferred filename patterns, qBittorrent connection, and the HTTP download
   destination. JavDB/Keepshare is available as the HTTP provider without a
   PikPak account.
5. Optionally configure **Settings → StashApp** for local-library synchronization, Watchlist-tag synchronization, missing-file scans, and path remapping.
6. Review automation schedules before enabling unattended scraping or downloading.

## Torrent and HTTP downloads

Manual **Search** and **Search + Download** requests combine results from the
configured Sukebei/Nyaa-compatible Torrent provider and the modular HTTP
provider system. HTTP discovery currently uses JavDB release pages and their
public Keepshare/PikPak shares. Search results identify the transport clearly
and provide separate **Open JavDB** and **Open Keepshare** links. JAVBeacon
stores both links with the HTTP download, so they remain available in
**Download Activity** during progress, after completion, and when retrying a
failure.

JavDB matching is intentionally strict: the release ID must match
case-insensitively, with or without separators such as the hyphen, and the
JavDB release date must be within 60 days of the date stored in JAVBeacon. For
every matching release page, JAVBeacon collects every distinct Keepshare link
and inspects its actual downloadable files. Candidate priority is:

1. Files whose names contain a configured **Preferred filename pattern**.
2. Non-`-U` filenames before equivalent `-U` variants.
3. Larger matching video files before smaller alternatives.

If no file matches a preferred pattern, JAVBeacon automatically continues with
the normal non-`-U` and filesize ordering. Preferred patterns are therefore a
ranking preference, not a requirement that hides otherwise usable HTTP
downloads.

Under **Settings → Downloads → HTTP Downloads**, configure:

- **JavDB URL** — the HTTP discovery provider base URL.
- **Download folder** — where completed HTTP videos are written.
- **Parallel HTTP downloads** — the maximum simultaneous HTTP transfers.
- **Stalled torrent fallback delay** — how long a non-progressing torrent with
  no seeders or no recorded completed peer may wait before HTTP is attempted;
  the default is eight hours.

A release uses Torrent first by default, with HTTP as its fallback. Release
Details can make HTTP primary for an individual release. Completed HTTP files
are named `<RELEASE-ID>.mp4`; if that name exists, JAVBeacon appends `-0`,
`-1`, and so on. The HTTP tab in Download Activity reports transferred bytes,
progress, ETA, completion or failure state, and offers retry for failed items.
Blocked or expired public shares and provider data-center restrictions are
reported as explicit failures instead of producing an HTML file.

## Configuration

Most configuration is stored in the active database and managed from the web interface. The following environment variables control bootstrap and database connectivity:

| Variable | Purpose | Default |
| --- | --- | --- |
| `JAVBEACON_LISTEN` | HTTP listen address | `:8080` |
| `TZ` | IANA timezone the container (and therefore all "server local time" schedules) runs in | `UTC` |
| `PUID` | Host user ID used for files in the Compose application-data mount | `1000` |
| `PGID` | Host group ID used for files in the Compose application-data mount (`GUID` is accepted as a legacy alias) | `1000` |
| `JAVBEACON_DB` | SQLite database path | `data/javbeacon.db` |
| `JAVBEACON_COVERS` | Initial cover-cache directory | `data/covers` |
| `JAVBEACON_SCREENSHOTS` | Initial screenshot-cache directory | `data/screenshots`; Compose uses `/app/data/screenshots` |
| `JAVBEACON_INITIAL_USERNAME` | Username created on the first start | `admin` |
| `JAVBEACON_INITIAL_PASSWORD` | Password created on the first start | `changeme123` |
| `JAVBEACON_API_KEY` | Initial API key (Settings → General → API access generates a random one automatically if this is unset, and is the authoritative source thereafter) | unset (random key auto-generated on first start) |
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

### Large-library performance

JAVBeacon automatically installs its PostgreSQL search and sorting indexes on
startup, including the `pg_trgm` extension used for fast partial title,
release-ID, actress, studio, label, tag, and story matching. The first start
after upgrading a large existing database can therefore take a little longer
while PostgreSQL builds those indexes; later starts reuse them.

The Release Library uses lightweight card responses, cursor pagination,
short-lived exact-count caching, and a bounded metadata-suggestion cache.
These caches live inside JAVBeacon and invalidate as data changes, so the
recommended single-instance Compose stack does not need Redis. PostgreSQL 18,
JAVBeacon, and Byparr remain the complete supported stack.

## Credential recovery

Stop the running application and use either or both reset flags:

```bash
go run ./cmd/javbeacon --reset-username my-user
go run ./cmd/javbeacon --reset-password 'a-new-long-password'
```

For a Docker deployment, run the same binary inside the container with the persistent data volume attached.

## StashApp scraper integration

JAVBeacon can act as a fast local data source for the community
[JavLibrary Python scraper](https://github.com/stashapp/CommunityScrapers/tree/master/scrapers/JavLibrary_python)
used by StashApp. That scraper normally fetches every scene from JavLibrary
itself, which - because of JavLibrary's Cloudflare protection - requires
Byparr and can be slow. If a release is already in your JAVBeacon library
(JAVBeacon scraped it from JavLibrary itself), the scraper can be configured
to fetch that already-scraped data straight from JAVBeacon's API instead,
and only fall back to scraping JavLibrary directly when JAVBeacon doesn't
have the release yet.

To enable it:

1. Open Settings → General → API access in JAVBeacon and copy the API key
   shown there (a random key is generated automatically the first time
   JAVBeacon starts, so one is already there - use Regenerate if you'd
   rather issue a new one). Without a key, JAVBeacon's `/api/*` endpoints
   only accept an authenticated browser session, which a script cannot use.
2. In `JavLibrary_python.py`, set `JAVBEACON_ENABLED = True` and point
   `JAVBEACON_URL` and `JAVBEACON_API_KEY` at your JAVBeacon instance and the
   key from step 1.
3. Leave `JAVBEACON_FALLBACK_TO_JAVLIBRARY = True` (the default) so scenes
   JAVBeacon doesn't have yet still scrape normally through Byparr.

This mode only draws on releases whose `source` is JavLibrary, and skips the
scraper's separate Japanese-alias lookup (JAVBeacon doesn't store a
performer's Japanese alias) - the same trade-off the scraper's own
`IGNORE_ALIASES` setting already offers for speed.

It relies on two small, additive `/api/releases` query parameters: an exact,
case-insensitive `video_id` filter, and `search` matching a release's stored
scraper ID and product URL in addition to its existing fields (title, video
ID, actress, studio, tag). Both are safe to use directly if you build your
own tooling against JAVBeacon's API.

When the StashApp local-library sync matches a release, its API object also
contains `stash_file_path`: the complete path of the first video file attached
to that StashApp scene. API clients such as JAVBeaconSubs can resolve a file
back to its release with the exact, case-insensitive `stash_file_path` query
parameter, for example
`/api/releases?stash_file_path=%2Flibrary%2FJAV%2FSSIS-001.mp4`.

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
`VERSION=1.0.27` is released as `v1.0.27`.

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
