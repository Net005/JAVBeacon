# JAVBeacon realtime Stash sync

1. Copy this directory into Stash's `plugins` directory.
2. Edit `javbeacon-realtime.yml`: set `javbeacon_url` to an address reachable
   from the Stash container and set `webhook_secret` to the same random value
   saved in JAVBeacon under **Settings → StashApp → Changed-scene sync**.
3. In Stash, open **Settings → Plugins** and reload plugins.
4. Enable realtime sync in JAVBeacon.

Use **Settings → Tasks → Test JAVBeacon connection** in Stash to verify the
container URL and dedicated webhook secret. The task writes the request ID,
endpoint, elapsed time, and result to Stash's debug log; JAVBeacon records the
same request ID in its own log so a connection can be traced end to end.

The hook watches scene create, update, and delete events. It only queues the
scene ID; JAVBeacon then fetches the authoritative scene from Stash. Rapid
updates to one scene are coalesced and transient failures are retried. The
scheduled full local-library sync should remain enabled as reconciliation.
