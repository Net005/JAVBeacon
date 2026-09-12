# JAVBeacon Stash integration

This plugin provides two integrations:

- realtime scene-change notifications from Stash to JAVBeacon;
- a native subtitle button on each Stash scene page that submits the scene's
  full file path to JAVBeacon-Subs.
- an always-visible Watchlist toggle on Stash scene cards.

The subtitle request runs in Python inside the Stash plugin process. It does
not require `curl`, does not call JAVBeacon-Subs from the browser, and does
not expose the API token to the scene page.

## Install

1. Copy this entire directory into Stash's `plugins` directory.
2. In Stash, open **Settings → Plugins** and reload plugins.
3. Open this plugin's settings and set:
   - **JAVBeacon URL** to an address reachable from the Stash server, normally
     `http://javbeacon:8080` when both applications share a Docker network;
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

For matching scene paths, the action appears in the scene action row and at
the bottom-right of cards on the scene overview. **+ CC** requests subtitles
when Stash reports no linked caption or subtitle tracks. When subtitles are
already linked, it changes to a disabled **✓ CC** completion indicator.
Selecting **+ CC** resolves the scene's first full file path from Stash on the
server and submits:

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
When **Scene path filters** is configured, the action is shown only when the
first scene file path contains at least one configured fragment. The same
check is enforced by the server when the request is submitted.

## Realtime sync

Use **Settings → Tasks → Test JAVBeacon connection** in Stash to verify the
URL and dedicated webhook secret configured in the plugin UI. The task writes the request ID,
endpoint, elapsed time, and result to Stash's debug log; JAVBeacon records the
same request ID in its own log so a connection can be traced end to end.

The hook watches scene create, update, and delete events. It only queues the
scene ID; JAVBeacon then fetches the authoritative scene from Stash. Rapid
updates to one scene are coalesced and transient failures are retried. The
scheduled full local-library sync should remain enabled as reconciliation.
