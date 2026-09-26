# JAVBeacon ↔ Jellyfin

This integration keeps Jellyfin and StashApp decoupled:

```text
Jellyfin plugin → JAVBeacon REST API → StashApp GraphQL
```

Jellyfin knows only the JAVBeacon URL/API key. JAVBeacon owns path and release matching, Stash credentials and scene IDs, elapsed-play calculation, resume/play-duration checkpoints, completion thresholds, play-count deduplication, and O-count mutations.

## Compatibility

The default build targets Jellyfin **12.0.0 / .NET 10**, matching Atlantis.
The plugin has been compiled against the final Jellyfin 12 reference assemblies;
its metadata, playback, collection, and library-scan adapters require no
12-specific source changes.

```bash
curl -sS https://your-jellyfin.example/System/Info/Public
./integrations/jellyfin/build.sh
```

The REST client, DTOs, and JAVBeacon-owned playback engine remain isolated from
the Jellyfin event adapter. A legacy Jellyfin 10.11 build can still be produced
explicitly when needed:

```bash
TARGET_FRAMEWORK=net9.0 JELLYFIN_VERSION=10.11.11 ./integrations/jellyfin/build.sh
```

## Build and install

Requirements: .NET 10 SDK, or Docker.

```bash
./integrations/jellyfin/build.sh
# or
docker build --output type=local,dest=integrations/jellyfin/dist integrations/jellyfin
```

Stop Jellyfin, create `/config/plugins/JAVBeacon`, copy the contents of `integrations/jellyfin/dist` into it, and start Jellyfin. In Dashboard → Plugins → JAVBeacon configure:

- JAVBeacon URL reachable from the Jellyfin container (the public HTTPS URL is fine).
- JAVBeacon API key.
- Metadata and playback switches.
- Comma-separated Jellyfin user IDs to track; blank tracks all users.
- Optional Stash Watchlist collection synchronization and its collection name.
- Whether Stash scene changes should queue a Jellyfin library scan, plus the
  polling interval (minimum 15 seconds).

In the target movie library, enable JAVBeacon as both a movie metadata provider
and image provider, put it ahead of generic internet providers, and refresh
metadata with **Replace existing images** enabled. Automatic lookup sends the
full Jellyfin path first, then falls back to the parsed release code. Jellyfin
Identify uses JAVBeacon search. Successful matches persist both `JAVBeacon`
release ID and `Stash` scene ID in Provider IDs.

The movie detail page also exposes **JAVBeacon Release** as a custom external
ID and includes a direct external link to the full JAVBeacon `/release/{id}`
page, built from the URL configured in the plugin. No API key is placed in the
link.

Jellyfin's title and original title are set to the public JAV release ID (for
example `ABC-123`). The JAVBeacon release title, with that leading ID and its
separator removed, becomes the Jellyfin description. The JAVBeacon cover is
the Primary image. Locally cached JAVBeacon screenshots are offered first as
Backdrops, with the cover also offered last as a secondary/fallback Backdrop.
Source-site image URLs and credentials are never exposed to Jellyfin.

## JAVBeacon settings

Defaults are stored in JAVBeacon settings and can be changed through `PUT /api/settings`:

| Setting | Default | Meaning |
|---|---:|---|
| `jellyfin_checkpoint_seconds` | 30 | Unforwarded watched time before a Stash checkpoint |
| `jellyfin_max_checkpoint_gap_seconds` | 120 | Maximum wall-time credited across a missing progress event |
| `jellyfin_completion_percent` | 80 | Watched percentage that adds one Stash play |
| `jellyfin_completion_remaining_seconds` | 600 | Remaining-time completion rule for media longer than this value |
| `jellyfin_path_remaps` | `[]` | JSON `from`/`to` mount-prefix mappings from Jellyfin paths to Stash paths |

For example, if Jellyfin reports `/videos/jav/ABC-123.mp4` while Stash reports
`/media/jav/ABC-123.mp4`, save `[{"from":"/videos","to":"/media"}]` in
`jellyfin_path_remaps`. Exact direct and remapped paths are tried before the
release-code fallback.

Playback session state is durable in both SQLite and PostgreSQL. Stash failures leave unforwarded seconds pending so a repeated/later event can retry without double-counting.

## Watchlist collection and Stash-triggered scans

Enable **Synchronize the Stash Watchlist tag to a Jellyfin collection** in the
plugin and choose a collection name (default `Watchlist`). JAVBeacon reads the
configured `stash_watchlist_tag_id` from Stash, exposes only matching release
IDs to Jellyfin, and the plugin adds or removes JAVBeacon-backed movies until
the native Jellyfin collection matches. Unrelated manually-added collection
members are preserved. Managed Watchlist movies are stored newest-first using
the time each release was most recently added to Watchlist; manual members stay
after the managed entries in their existing relative order.

Realtime Stash scene-create/update/delete hooks advance JAVBeacon's library
revision. The plugin detects that revision and queues Jellyfin's native library
scan. A successful scheduled Stash local-library sync also advances it, which
catches changes made while realtime hooks were unavailable. The plugin queues
one initial scan after startup, then checks at the configured interval.

## Saved filter set collections

Enable **Create a collection for every saved filter set in JAVBeacon** to get
one Jellyfin collection per saved filter set from the Release Library (the
same filter sets behind the toolbar's saved-filter-sets menu), automatically
created, kept in sync, and renamed/removed when the filter set is renamed or
deleted. Each collection's membership and sort order come straight from
JAVBeacon's own filter+sort engine - the server resolves the filter set to an
already-sorted list of matching local, Stash-linked release IDs, so the
collection always matches what the Release Library itself would show for
that saved filter set, with no separate sorting logic in the plugin. An
optional prefix (for example `JAVBeacon: `) can be prepended to every such
collection's name to tell them apart from manually-created ones.

## Catch-up scheduled tasks

Two scheduled tasks appear under Jellyfin's own Scheduled Tasks page,
category "JAVBeacon":

- **Sync watched status from StashApp** - runs the same watched-status
  reconciliation as the "Sync watched status from StashApp" setting, once.
- **Resync JAVBeacon collections (catch-up)** - forces a full resync of the
  Watchlist collection, every saved-filter-set collection, and watched
  status, independent of the continuous background poll's cached revision.
  Runs every 6 hours by default and can also be triggered manually ("Run
  Now") or given its own custom schedule. This exists as a safety net: the
  background loop already reacts to Stash/JAVBeacon changes roughly every
  `LibrarySyncIntervalSeconds`, but a missed poll (Jellyfin restart mid-cycle,
  a JAVBeacon outage, the plugin reloading) has no other way to catch up.

## Stash metadata/image fallback

When a release is linked to a StashApp scene but JAVBeacon's own scrape is
incomplete (no title/overview, studio, performers, genres, or cover image),
the metadata endpoint fills only the missing fields directly from that Stash
scene's title/details/studio/performers/tags and screenshot - it never
overrides anything JAVBeacon already has. The Stash screenshot, when used, is
proxied through JAVBeacon (`/api/v1/integrations/jellyfin/releases/{id}/stash-cover`)
so the Stash base URL and API key never reach Jellyfin.

## API smoke tests

Use placeholders; do not commit keys:

```bash
curl -H 'Authorization: Bearer JAVBEACON_KEY' \
  -H 'Content-Type: application/json' \
  -d '{"path":"/media/ABC-123.mp4"}' \
  https://javbeacon.example/api/v1/media/match

curl -H 'Authorization: Bearer JAVBEACON_KEY' \
  'https://javbeacon.example/api/v1/integrations/jellyfin/search?q=ABC-123'
```

Run backend verification with:

```bash
GOCACHE=/tmp/javbeacon-go-cache GOFLAGS=-mod=mod go test ./internal/jellyfin
GOCACHE=/tmp/javbeacon-go-cache GOFLAGS=-mod=mod go test ./...
```

## Optional Jellyfin Web panel

`web/javbeacon-activity.js` adds O count, play count, played duration, and a
**+1 O** button to JAVBeacon-backed item pages. Jellyfin Web has no stable
first-party arbitrary UI-extension API, so add this file as one enabled script
in [Jellyfin JavaScript Injector](https://github.com/n00bcodr/Jellyfin-JavaScript-Injector),
then hard-refresh Jellyfin Web. The script accepts Jellyfin 12's snake_case API
fields as well as camel/Pascal case and prevents concurrent MutationObserver
renders from duplicating the panel. It calls only the plugin's authenticated
`/JAVBeacon/items/{itemId}/activity` and `/o` endpoints; it never receives the
JAVBeacon key or any Stash credential.
