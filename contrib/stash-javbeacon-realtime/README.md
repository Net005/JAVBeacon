# JAVBeacon Stash integration

This plugin provides two integrations:

- realtime scene-change notifications from Stash to JAVBeacon;
- a native subtitle button on each Stash scene page that submits the scene's
  full file path to JAVBeacon-Subs.
- a beacon button beside **+ CC** that opens the scene's exact JAVBeacon
  release page in a new tab.
- an always-visible Watchlist toggle on Stash scene cards.
- scene details beneath the scene ID on Stash scene cards, with a compact
  two-line preview, full-text hover tooltip, and click-to-expand display.
- native video cover preview and large seek-bar thumbnail preview on the scene player (see
  **Player previews** below).

The subtitle request runs in Python inside the Stash plugin process. It does
not require `curl`, does not call JAVBeacon-Subs from the browser, and does
not expose the API token to the scene page.

## Install

1. Copy this entire directory into Stash's `plugins` directory.
2. In Stash, open **Settings → Plugins** and reload plugins.
3. Open this plugin's settings and set:
   - **JAVBeacon URL** to an address reachable from the Stash server, normally
     `http://javbeacon:8080` when both applications share a Docker network;
   - optionally set **JAVBeacon browser URL** (for example
     `https://jav.example.com`) when the server URL is Docker-internal;
   - **JAVBeacon webhook secret** to the same dedicated secret saved in
     JAVBeacon under **Settings → StashApp → Changed-scene sync**;
   - optionally adjust **JAVBeacon request timeout** from its 10-second default;
   - **JAVBeacon-Subs base URL** to the address reachable from Stash, such as
     `https://subs.example.com`;
   - **JAVBeacon-Subs API token** to the bearer token issued by JAVBeacon-Subs.
   - **Watchlist tag ID** to the Stash tag ID that represents your Watchlist.
   - Optionally set **Scene path filters** to one or more partial paths. Matches
     are case-insensitive and may be separated by new lines, commas, or
     semicolons. Leave it blank to allow all scene paths.
4. Reload the Stash page after installing or updating the plugin.

The scene-card Watchlist control is independent of subtitle path filtering.
It always remains visible: **+ Watchlist** adds the configured tag and
**✓ Watchlist** clearly shows membership and removes the tag when selected.
All other tags on the scene are preserved. If no Watchlist tag ID is configured,
the control remains visible but disabled with a configuration hint.

Scene cards do not issue subtitle or Watchlist status requests while a page is
loading. When card data does not already contain that status, the plugin checks
the individual scene when the pointer first enters its card (or when an action
is clicked). Results are remembered for the browser session, so the same scene
is not checked again. The scene details page still checks subtitle status when
needed. This keeps large scene pages and dashboard carousels from blocking
navigation with per-card queries.

When Stash includes scene details in its card data, the story appears
immediately below the scene ID. Otherwise it is included in that same deferred
card lookup. The preview uses the full card width and is clamped to two lines;
hovering shows the complete text in a tooltip, while clicking (or pressing
Enter/Space) toggles the full story without opening the scene.

Plugin settings are read without writing the partial configuration response to
Stash's shared Apollo cache, avoiding repeated cache-merge warnings on large
card pages.

For matching scene paths, the action appears in the scene action row and,
on scene cards, merged directly into Stash's own popover-icon row (tag
count, gallery count, etc.) rather than on a line of its own - Watchlist
sits immediately left of those icons, +CC sits flush against the row's
right edge, and the card's overall height is unchanged. **+ CC** requests subtitles
when Stash reports no linked caption or subtitle tracks. When subtitles are
already linked, it changes to **✓ CC**, and on scene cards its text color
reports freshness at a glance, without a click: dark green means a
JAVBeacon-Subs sidecar exists and matches the current transcription/
translation backend (up to date); orange means a sidecar exists but predates
the current backend, or its freshness could not be determined; red means
captions exist in Stash but no JAVBeacon-Subs sidecar was found at all (not
produced by the AI pipeline - a manually added or externally sourced `.srt`)
and should be replaced; no color at all means the scene has no subtitles.
Selecting it when subtitles already exist first checks the scene's
`.en.srt.json` sidecar (written by
JAVBeacon-Subs next to the video) against JAVBeacon-Subs's current
transcription/translation backend, then confirms with wording matching what
it found:

- **No sidecar file** - the existing subtitles predate version tracking (an
  older subtitle translator). Treated as outdated; a normal confirmation
  offers to replace them.
- **Sidecar found, backend outdated** - a normal confirmation names both the
  sidecar's recorded backend and JAVBeacon-Subs's current one and offers to
  replace them.
- **Sidecar found, backend already current** - a confirmation states the
  subtitles are already up to date and that regenerating them is **not**
  recommended. Only a second, explicitly labeled "FORCE OVERWRITE" warning
  confirmation proceeds past that point.
- **Freshness could not be determined** (JAVBeacon-Subs has no
  `/api/v1/backends` endpoint yet, or the check failed) - falls back to the
  original plain "replace the existing subtitles?" confirmation so nothing
  regresses on older JAVBeacon-Subs deployments.

Cancelling at any point sends no request. Selecting **+ CC** resolves the
scene's first full file path from Stash on the server and submits:

```json
{
  "inputs": ["/collections/jav/NSPS-642.mp4"],
  "recursive": false,
  "overwrite": false,
  "auto_detect_release": true,
  "release_within_days": 0,
  "debug_mode": true,
  "keep_japanese": true,
  "write_ass": false
}
```

The request is sent to `<base URL>/api/v1/jobs` with the configured token.
The base URL may also include `/api/v1/jobs`; the plugin will not append it
twice.

Every request option shown above is available separately in the plugin
settings, with the values above used as server-side defaults. **Job options
(JSON)** can override those fields or add fields supported by JAVBeacon-Subs.
For safety, `inputs` is always replaced with the current scene's full path.
The button sends an explicit `overwrite: false` for new subtitles or
`overwrite: true` after replacement is confirmed. This choice takes precedence
over the plugin default and JSON job options for that request.
When **Scene path filters** is configured, the action is shown only when the
first scene file path contains at least one configured fragment. The same
check is enforced by the server when the request is submitted.

The backend-freshness check above calls `GET <JAVBeacon-Subs base
URL>/api/v1/backends` with the same bearer token as job submission, expecting
a JSON object naming the backends JAVBeacon-Subs currently uses for new jobs:

```json
{
  "transcription_backend": "Qwen/Qwen3-ASR-1.7B",
  "translation_backend": "gpt-5.6-luna"
}
```

This endpoint does not need to exist for the plugin to work: a missing
endpoint, a non-2xx response, or a malformed body are all treated as
"freshness unknown" and fall back to the plain confirmation prompt.

On scene detail pages, the yellow beacon icon appears to the right of **+ CC**
with extra separation between the two actions. It securely resolves the
scene's linked release through JAVBeacon and opens its exact release page. The
beacon remains available when subtitle path filters hide **+ CC**. The
server-facing URL and webhook secret stay inside the Stash plugin process;
only the configured browser URL and final release path reach the page.

## Player previews

On a scene details page, hovering the cover before playback starts plays the
same native MP4 preview that Stash serves to its scene grid (`scene.paths.preview`).
The preview is muted, loops, and stops when the pointer leaves or normal
playback starts. It loads only when needed. If Stash has no playable preview,
the cover falls back to cycling through the scene's sprite screenshots.

Moving over the seek bar still enlarges Stash's own thumbnail preview into the
player area. The cover preview and seek-bar preview leave the normal player
controls visible and interactive. The plugin adds one inert overlay beneath
the control bar; it does not replace or resize Stash's player.

Settings:

- **Enable cover-area video preview** (default on)
- **Enable large seek-bar preview** (default on)
- **Cover hover delay (ms)** (default 400)
- **Sprite fallback interval (ms)** (default 700, minimum 100)

Scenes without generated preview media keep the regular cover image.

## Realtime sync

Use **Settings → Tasks → Test JAVBeacon connection** in Stash to verify the
URL and dedicated webhook secret configured in the plugin UI. The task writes the request ID,
endpoint, elapsed time, and result to Stash's debug log; JAVBeacon records the
same request ID in its own log so a connection can be traced end to end.

The server hook watches scene create, update, and delete events. Stash's
dedicated play, O, and activity mutations do not emit that hook, so the bundled
browser component also detects their successful GraphQL responses and invokes
the same server-side plugin operation. The webhook secret remains server-side.
The plugin only queues the scene ID; JAVBeacon then fetches the authoritative
scene from Stash, including its complete current play and O event lists. New
events therefore reach JAVBeacon without waiting for a full scan.
JAVBeacon's history is authoritative and append-only: deleting or resetting
history in Stash never removes or modifies events already retained by
JAVBeacon. Rapid updates to one scene are coalesced and transient failures are
retried. The scheduled full local-library sync should remain enabled as
reconciliation.
