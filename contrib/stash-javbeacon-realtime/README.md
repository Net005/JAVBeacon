# JAVBeacon Stash integration

This plugin provides two integrations:

- realtime scene-change notifications from Stash to JAVBeacon;
- a native subtitle button on each Stash scene page that submits the scene's
  full file path to JAVBeacon-Subs.

The subtitle request runs in Python inside the Stash plugin process. It does
not require `curl`, does not call JAVBeacon-Subs from the browser, and does
not expose the API token to the scene page.

## Install

1. Copy this entire directory into Stash's `plugins` directory.
2. Edit `javbeacon-realtime.yml`: set `javbeacon_url` to an address
   reachable from the Stash container and set `webhook_secret` to the same
   random value saved in JAVBeacon under **Settings → StashApp → Changed-scene
   sync**.
3. In Stash, open **Settings → Plugins** and reload plugins.
4. Open this plugin's settings and set:
   - **JAVBeacon-Subs base URL** to the address reachable from Stash, such as
     `https://subs.example.com`;
   - **JAVBeacon-Subs API token** to the bearer token issued by JAVBeacon-Subs.
5. Reload the Stash page after installing or updating the plugin.

The closed-caption icon appears in the scene action row. Selecting it resolves
the scene's first full file path from Stash on the server and submits:

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

## Realtime sync

Use **Settings → Tasks → Test JAVBeacon connection** in Stash to verify the
container URL and dedicated webhook secret. The task writes the request ID,
endpoint, elapsed time, and result to Stash's debug log; JAVBeacon records the
same request ID in its own log so a connection can be traced end to end.

The hook watches scene create, update, and delete events. It only queues the
scene ID; JAVBeacon then fetches the authoritative scene from Stash. Rapid
updates to one scene are coalesced and transient failures are retried. The
scheduled full local-library sync should remain enabled as reconciliation.
