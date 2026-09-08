# JAVBeacon realtime Stash sync

1. Copy this directory into Stash's `plugins` directory.
2. Edit `javbeacon-realtime.yml`: set `javbeacon_url` to an address reachable
   from the Stash container and set `webhook_secret` to the same random value
   saved in JAVBeacon under **Settings → StashApp → Changed-scene sync**.
3. In Stash, open **Settings → Plugins** and reload plugins.
4. Enable realtime sync in JAVBeacon.

The hook watches scene create, update, and delete events. It only queues the
scene ID; JAVBeacon then fetches the authoritative scene from Stash. Rapid
updates to one scene are coalesced and transient failures are retried. The
scheduled full local-library sync should remain enabled as reconciliation.
