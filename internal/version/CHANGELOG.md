# Changelog

All notable user-facing changes to JAVBeacon are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and JAVBeacon uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.0.160] - 2026-09-12

### Fixed

- Download Activity's HTTP/Torrent tab now defaults to HTTP for anyone who
  has never explicitly chosen a tab, instead of silently defaulting to
  Torrent. Previously that Torrent default could also get written back to
  your saved preferences the first time the app loaded, so it kept
  "remembering" Torrent even though it was never an actual choice.
  Explicitly selecting a tab already saved and restored correctly across
  app/browser restarts - if Download Monitoring still opens on Torrent
  after updating, click the HTTP tab once and it will be remembered from
  then on.

## [1.0.159] - 2026-09-11

### Changed

- GIGA (Akiba-Web) release covers now prefer the large "_l" cover image
  GIGA also serves alongside its default small "_s" one (for example
  `pac_l.jpg` next to `pac_s.jpg`), used whenever that larger image is
  actually available and falling back to the small cover otherwise. This
  applies to every release detail fetch, so Quick and Full refresh both
  pick it up automatically for releases already in your library: each
  time a GIGA release is re-checked, its cached cover is compared against
  the newly scraped image and replaced if it changed - no separate
  backfill step is needed, a Full refresh (or a Quick refresh reaching the
  release's page) is what upgrades an existing small cover to the large
  one over time.

## [1.0.158] - 2026-09-11

### Fixed

- Once a release's download method was forced to Torrent only or HTTP
  only from Monitoring's "Force Search + Download" bulk action, nothing
  in the app could ever clear it again - not even switching Settings ->
  Downloads -> Default download method to the opposite "Only" mode. The
  release quietly kept using its old forced transport forever, including
  from unrelated actions like a plain Release Library bulk search, which
  is why HTTP-only downloads could still show Torrent search failures
  against Sukebei/Nyaa. The same "Force Search + Download" dialog now
  offers "Use global default", which clears the forced override on the
  selected releases so your Default download method setting governs them
  again.

## [1.0.157] - 2026-09-10

### Added

- New scheduled job: **Release Upgrade Schedule**. Once a day (time
  configurable under Settings → Downloads), it re-checks every already
  HTTP-downloaded release with a release date within 30 days of today (past
  or future) whose saved file does not match your #1 (highest-priority)
  preferred filename pattern, re-runs the normal Search + Download against
  JavDB/Keepshare, and — only if a result is now found matching that #1
  pattern — deletes the old video and its stale `.en.srt`, `.en.srt.json`,
  `.ja.srt`, `.ja.srt.json`, and `.subtitles.json` sibling files and
  downloads the replacement. Disabled by default; enable it and set a run
  time under Settings → Downloads → Release Upgrade Schedule. Only
  considers releases with a known HTTP download history, and only
  HTTP-transport downloads (torrent-sourced releases are not upgraded). A
  live status panel and a history of what each run checked, upgraded, and
  skipped are shown on the Download Activity page, alongside a "Run now"
  button and the usual scheduled-job log lines.

## [1.0.156] - 2026-09-10

### Fixed

- Release Details could show different data depending on how it was
  opened - e.g. Duration and the "Stash history" play badge present when
  opened from Monitoring but missing when opened for the same release from
  the Release Library - even with a fully cleared cache. Opening a release
  that was already sitting in the Release Library's lightweight card list
  painted an instant preview from that (deliberately trimmed) card data,
  then only patched a couple of small pieces of the dialog once the full
  release fetch resolved, leaving fields such as Duration, Story, and the
  release date range that only the full record carries stuck at the
  incomplete preview's values for as long as the dialog stayed open.
  Release Details now always finishes with a full re-render once the
  authoritative fetch resolves, so every field reflects the same complete
  record regardless of entry point.
- The Discover "Status" badges (download progress, local/library status,
  Stash watch history) in Release Details were written into a DOM node
  that a moment earlier had already been detached from the page as part of
  building the visible "Status" panel, so after a release's first render
  those badges could never be refreshed again for as long as the dialog
  stayed open - they would go stale, disappear, or (once a background
  update did land) reappear duplicated in the wrong spot. The Status panel
  is now updated directly, in place, every time.

## [1.0.155] - 2026-09-10

### Fixed

- The served app shell's `app.js`/`app.css` URLs carried a frozen
  cache-busting placeholder that never actually changed between releases,
  so a browser that had already cached that exact URL kept serving the
  stale script/stylesheet indefinitely - through any number of later
  releases - until its cache was cleared by hand. This is very likely the
  cause of "it looks different depending on where I open it from" reports:
  different tabs/sessions could each be running a different cached build
  at once. The asset URLs now carry the actual running version and change
  on every release, the same way `/assets/` already forces revalidation.
  If the release detail page still looks inconsistent after updating to
  this version, a hard refresh (Ctrl/Cmd+Shift+R) clears out the
  previously-stuck cached copy.

## [1.0.154] - 2026-09-10

### Fixed

- The release detail STATUS row's Played / O-Count badge could stay
  permanently hidden even for a release with real Stash watch history: once
  a release was cached from an earlier Release Library visit (where card
  data intentionally omits playback stats to keep the grid light), opening
  its detail view rendered that stale, zeroed-out data immediately and never
  refreshed it after the full detail fetch completed. The DISCOVER badges
  (download status, local status, and Played / O-Count) are now refreshed
  with the fetched data the same way the TRACKING buttons already are.

### Added

- The download status badge now distinguishes "queued" from "downloading":
  an HTTP download waiting for a free concurrency slot now shows "HTTP
  queued" instead of "HTTP downloading", and hovering it shows its position
  in the queue and its priority.
- Search + Download now warns before starting a search that duplicates one
  already in progress for the same release (searching, waiting to search,
  queued, or actively downloading), showing its status and letting you
  proceed anyway or cancel, instead of silently starting a redundant search.

## [1.0.153] - 2026-09-10

### Fixed

- Download Activity forgot which status tab (Downloading / HTTP /
  Stalled / Downloaded / Failed) was open on reload or app restart, always
  resetting to "Downloading" — which also made that tab's remembered sort
  direction look like it had been forgotten, since the sort was being
  restored for a different tab than the one being viewed. The active
  status tab is now persisted the same way the other Download Activity
  preferences already are.
- The release detail STATUS row could hide the Played / O-Count badge:
  once a Downloaded/Downloading badge was also present, the three badges
  no longer fit on one row, and the wrapped second row was clipped by the
  card's fixed height. Badges are now sized to fit three per row.
- Removed leftover "Allow non-preferred filename(s)" controls that should
  have been fully removed in 1.0.152 along with the concept itself: the
  Release Library bulk "Monitor + download" dialog, the Download Activity
  bulk bar, and the Missing Library Files bulk bar. The Download Activity
  one was a live regression — deleting selected downloads always failed
  with a "json: unknown field" error, since the checkbox's value was still
  being sent to an endpoint that no longer accepts it.

## [1.0.152] - 2026-09-10

### Changed

- Downloading (both HTTP and torrent) no longer hard-requires a preferred
  filename match to proceed. "Preferred filename patterns" is now purely a
  priority-ranking signal: a result matching one still gets first choice,
  but a release with no preferred-pattern match now falls back to the
  existing seed-based selection instead of being rejected outright. The
  now-redundant "preferred filename required" / "allow non-preferred
  filenames" concept has been removed from the matching logic, the
  per-release override, and the Missing Library Files apply endpoint.
- Cleaned up the "Monitored releases" panel's table: the site name and its
  flags (Watchlist, Notify, Local ignored) are now shown as a clean site
  line with individually-wrapping flag chips, replacing the two-pill-box
  layout that wrapped badly.

### Added

- A "Reset local ignore" bulk action on the "Monitored releases" panel:
  clears the persistent "ignore StashApp Local" override for the selected
  releases, and for any of them that are now actually local (matched in
  StashApp), also takes that release off monitoring in the same action,
  since there is no remaining reason to keep searching for it. A release
  that is not local yet keeps its existing monitoring state.

## [1.0.151] - 2026-09-10

### Fixed

- Search + Download (and the "Monitor + download" bulk action) could queue
  a release a second time even while it already had an active
  (queued/downloading/processing) or completed download, creating a
  redundant duplicate download entry and running a wasted provider search.
  It's now denied outright unless the release's only download history is
  "failed", which can still always be retried. The scheduled monitored-
  release download job was checked and already guarded against this
  correctly.
- The Download Activity search field was case-sensitive on the app's
  PostgreSQL backend (SQLite's default collation happened to mask it
  there), so a search like "rbk" would not match "RBK". It now uses the
  same case-insensitive matching already used elsewhere in the app.

### Added

- Manual Search + Download now shows a notification explaining why a
  release was not queued (e.g. "is already downloading", "has already
  been downloaded") instead of always claiming it started in the
  background. Because this is easy to miss as a routine corner toast, it
  is shown as a more noticeable center-screen alert.

## [1.0.150] - 2026-09-09

### Added

- A Silo Server integration API (`GET /api/v1/integrations/silo/search`,
  `GET /api/v1/integrations/silo/releases/{id}`), backing a new
  `silo-plugin-metadata-javbeacon` metadata plugin for
  [Silo Server](https://github.com/Silo-Server). It reuses the existing
  Jellyfin integration's release-metadata service, so both integrations
  serve the same provider-agnostic DTO shape.

## [1.0.149] - 2026-09-09

### Fixed

- HTTP downloads never showed up in the Notifications page's Downloaded tab.
  Completing an HTTP download created its notification with the type
  "download_completed" instead of "downloaded" - the type the Downloaded
  tab actually filters on, and what torrent completions already used - so
  those notifications silently existed but never matched the tab. Both
  HTTP completion paths now use the correct type.
- The Download Failed tab had no dedicated sort option and always sorted
  by generic notification date. It now has its own "Download failed date"
  sort, used as that tab's default.
- Clearing a New Release notification (individually, via a selection, or
  the whole page) left the release's Notification setting on, so the
  exact same notification could reappear the next time releases were
  swept for newly-passed release dates. Clearing a New Release
  notification now also turns that release's Notification setting off.

### Changed

- The Notifications toolbar's "Clear selected" and "Clear this tab"
  buttons are replaced by a single button that relabels itself: "Clear
  all Notifications" clears everything on the current page when nothing
  is selected, "Clear notification" clears your selection when something
  is. This replaces the old "Clear this tab", which deleted every
  notification of that type in the database regardless of filters or
  page.
- Toggling a release's Notification setting off directly from a New
  Release tab card now also removes that release's entry from the list,
  instead of leaving a stale card behind until the next reload.
- The Notifications page's page-size selector now offers 250 and 500,
  in addition to 10/25/50/100.

## [1.0.148] - 2026-09-09

### Fixed

- Download priority did not actually control processing order end to end.
  It only decided which already-resolved HTTP download got the next free
  transfer slot; the Search + Download worker that runs before that point
  processed submitted batches strictly first-in-first-out, so a single
  urgent release triggered while a large low-priority batch was already
  running got stuck behind that entire batch regardless of its priority.
  The worker now keeps every pending release - from any source: a single
  "Search + Download now" click, a Release Library bulk action, or work
  resumed at startup - in one queue and always processes the lowest
  (most urgent) priority release next, so a high-priority release now
  genuinely jumps ahead of an already-running lower-priority backlog
  instead of waiting for all of it to finish first. This does not
  interrupt a release that is already actively searching or downloading -
  priority only ever decides what gets picked up next.

## [1.0.147] - 2026-09-09

### Added

- Download priority system: each download now gets a queue priority
  computed from its release's release date (newest/upcoming releases
  first, tapering off across four tiers down to a default priority for
  old or undated releases), shown on every Download Monitoring card. The
  Release Library's "Monitor + download" bulk dialog can override it with
  a fixed priority for the whole batch instead. The HTTP download
  concurrency queue now actually serves the lowest-priority-value
  download next instead of strict first-in-first-out, so a high-priority
  batch genuinely jumps the line for the next free download slot.
- A new "Not available" download state distinguishes "JavDB exact-matched
  the release but has no Keepshare/PikPak download link published yet"
  from a genuine failure - it gets its own Download Monitoring tab, its
  own styling, and is no longer counted as a failure. The known
  JavDB/Keepshare links are preserved and shown the same way they are for
  an active download.
- Download Monitoring tabs now each remember their own sort field and
  direction across restarts and page reloads: Failed defaults to
  newest-failed-first, Completed to newest-completed-first, Queued and In
  Progress to newest-added-first, and Downloading to soonest-ETA-first.
  Priority is also now available as a sort field on every tab.

### Changed

- JavDB and Keepshare searches now wait a randomized 3-7 seconds between
  requests, with adaptive backoff if a request looks throttled, and
  PikPak's API calls get a lighter equivalent cooldown - both to stop
  hammering either site during a large batch search.
- Authenticated PikPak share resolution now retries up to 3 times instead
  of giving up after 1 attempt, now that a retry reuses an
  already-restored file on the account instead of duplicating it.
- The loading message shown while download search results are still
  ranking candidates is now labeled "Waiting for enabled providers…"
  instead of a longer, easily-confused label.

### Fixed

- ffprobe's video integrity check used a `-xerror` flag that isn't a
  valid option in the deployed ffmpeg build, so every HTTP-downloaded
  video was silently rejected and automatically re-downloaded regardless
  of whether the file was actually valid. Replaced with the correct
  `-err_detect explode` flag.
- That automatic re-download was also marking the download "downloading"
  in the database before it had actually re-acquired an HTTP concurrency
  slot, which could make the Downloading count briefly exceed the
  configured `http_download_concurrency` limit. It's now marked "queued"
  until a slot is genuinely held.

## [1.0.146] - 2026-09-08

### Fixed

- Cover conforming (the live resize/slice of a JavLibrary/GIGA spread cover
  to Jellyfin's 1000x1500 Primary/Poster/Cover shape) was accidentally
  applied to `/covers/{id}`, the same endpoint JAVBeacon's own web UI uses
  everywhere - meaning the Release Library grid and release detail pages
  could show a cropped/padded cover instead of the cover exactly as
  scraped. `/covers/{id}` is now restored to always serve the cover exactly
  as cached, with no conforming, regardless of shape. A brand-new endpoint,
  `/covers/{id}/jellyfin-primary`, now carries the conforming feature
  exclusively - it's what Jellyfin's Primary/Poster/Cover image fetch uses.
  This is a server-side URL change only: the already-installed Jellyfin
  plugin needs no update or reinstall, since it already treats the cover
  URL it's given as opaque. Jellyfin's Backdrop image
  (`/covers/{id}/original`) was never affected by this bug and continues to
  serve the uncropped original.

## [1.0.145] - 2026-09-08

### Fixed

- JavLibrary and GIGA cover conforming now correctly recognizes a release's
  cover regardless of which domain actually hosts the image file.
  JavLibrary frequently hotlinks a release's cover from DMM's own CDN
  (`pics.dmm.co.jp`) instead of hosting it on javlibrary.com, and GIGA's own
  covers are hosted on `giga-web.jp` rather than `akiba-web.com` - since
  conforming was gated on the cover image's own URL, both of these were
  silently skipped and served uncropped, even though their dimensions
  matched perfectly. It's now gated on the release's own product/detail-page
  URL instead, which correctly identifies which site the release actually
  came from.

## [1.0.144] - 2026-09-08

### Fixed

- Clicking Watchlist, Notification, or Monitoring on a release in the
  Release Library grid no longer rebuilds the entire grid (destroying and
  recreating every card, then re-measuring every tag chip on every card to
  fit them) up to three times per click - once optimistically, again once
  the save confirms, and again ~500ms later when the change echoes back
  over the websocket. That toggle now only updates the one button that
  changed, in place, so it responds instantly regardless of how many
  releases are loaded. Actions that genuinely change more about a release
  (Search & Download, Update details, etc.) still refresh normally once
  their job completes.

## [1.0.143] - 2026-09-08

### Changed

- JavLibrary/GIGA cover conforming (slicing a two-panel spread down to the
  front cover, or padding an already-single-panel cover to Jellyfin's
  1000x1500 Primary/Poster/Cover size) now happens live, in memory, only at
  the moment a Primary image is actually requested - it's never written
  back to the on-disk cache file. The cached cover always stays exactly
  what was downloaded, so `/covers/{id}/original` (used for Jellyfin's
  Backdrop image) no longer needs a separately-cached original: it's simply
  the same file, always unconformed. This also means a cover cached before
  this feature existed, or by an older build, now conforms correctly the
  very next time it's requested - no re-download or "Update Details" needed.

### Added

- Both timeouts involved in JavLibrary/Byparr scraping are now configurable
  in Settings under Scraping → Byparr / FlareSolverr, and apply immediately
  without a restart: **Request timeout** (`byparr_request_timeout_seconds`,
  default 30s) bounds every request this scraper makes - a direct
  JavLibrary fetch and the call asking Byparr/FlareSolverr to solve one -
  and **Solve budget hint** (`byparr_solve_timeout_seconds`, default 75s) is
  the internal time allowance passed to the solver in that request. Raising
  the hint only helps if the request timeout above it is raised too.

## [1.0.142] - 2026-09-08

### Added

- Exposed Jellyfin playback thresholds in Settings under StashApp: `jellyfin_checkpoint_seconds`, `jellyfin_max_checkpoint_gap_seconds`, `jellyfin_completion_percent`, and `jellyfin_completion_remaining_seconds`, so you can tune replay/jump handling without editing config files.

## [1.0.141] - 2026-09-08

### Added

- Cover conforming now also covers GIGA release covers, in both shapes GIGA
  serves: older two-panel spread covers (like JavLibrary's) are sliced down
  to the front-cover panel, and newer already-single-panel covers are
  padded straight to Jellyfin's Primary/Poster/Cover size (1000x1500, 2:3)
  with no slicing. Every other GIGA cover shape, and every non-GIGA,
  non-JavLibrary source, is left untouched exactly as before.
- Jellyfin's Backdrop image now always uses the non-cropped, non-padded
  original cover instead of the conformed Primary/Poster/Cover version, for
  any release whose cover was sliced or padded (JavLibrary or GIGA). A
  cropped poster made a poor background; the original is now cached
  alongside the conformed poster and served from a new `/covers/{id}/original`
  endpoint that the Jellyfin plugin's backdrop image uses.

## [1.0.140] - 2026-09-08

### Added

- JavLibrary release covers that are a two-panel scan (back cover/text,
  spine, front cover) are now sliced down to just the front-cover panel and
  conformed to Jellyfin's Primary/Poster/Cover size (1000x1500, 2:3) when
  cached, padding rather than cropping further to hit the exact ratio.
  Already-single-panel covers and every other source are left untouched.

### Fixed

- HTTP downloads no longer fall back to a "-0", "-1", ... filename suffix
  and silently re-download a duplicate file when the destination already
  exists on disk. The existing file is now recognized immediately -
  skipping the fetch (and, for PikPak, an unnecessary account restore)
  entirely - and shown in Download Activity as "completed" with a distinct
  "file already existed" state, instead of looking like a normal transfer.

## [1.0.139] - 2026-09-08

### Fixed

- Fixed the realtime Stash connection test always sending `scene_id` in its
  request body, which the strict `/api/hooks/stash/test` decoder rejected
  with `json: unknown field "scene_id"`. The plugin script now omits it on
  test-mode requests, and the server accepts (and ignores) an optional
  `scene_id` there as well.

## [1.0.138] - 2026-09-08

### Fixed

- Fixed a nil-pointer panic in the realtime Stash webhook auth test
  (`TestStashRealtimeHookRequiresDedicatedSecret`) that was failing the
  release workflow's test gate and blocking the GHCR image build/push on
  every run since v1.0.133.

## [1.0.137] - 2026-09-08

### Fixed

- Fixed the Jellyfin movie image provider listing so images can be fetched for
  movie items even when a JAVBeacon provider ID is not yet attached.
- Hardened Jellyfin image URL normalization so `file:///...` image paths from
  Jellyfin identify/search are re-based through the configured JAVBeacon URL
  instead of being treated as local filesystem paths.

## [1.0.136] - 2026-09-08

### Fixed

- Fixed the Jellyfin image URL construction so cover and screenshot URLs now
  preserve configured URL prefixes instead of rewriting to the server root.

## [1.0.135] - 2026-09-08

### Added

- The Jellyfin metadata provider now supplies JAVBeacon cover art as the
  Primary image and locally cached release screenshots as preferred Backdrops,
  with the cover also available as a fallback Backdrop.
- Jellyfin movie pages now expose a JAVBeacon Release external ID and a direct
  link to the corresponding full JAVBeacon release page without including
  credentials in the URL.

### Changed

- Jellyfin titles and original titles now use the public JAV release ID, while
  the release title with its leading ID removed is used as the description.
- The synchronized Jellyfin Watchlist collection now preserves JAVBeacon's
  Watchlist-added order with the newest items first while retaining unrelated
  manual collection members afterward.

### Fixed

- Fixed the optional Jellyfin Web activity panel showing undefined O/play
  counts and `NaN` duration on Jellyfin 12 by accepting its snake-case response
  fields. Concurrent page mutations and repeated injection no longer create
  duplicate JAVBeacon panels.

## [1.0.134] - 2026-09-08

### Added

- Calmed the top-right activity area by removing its continuous pulse/scale animations and making its active, paused, and download colors follow the configured JAVBeacon theme.
- Added full-packet ffprobe validation before HTTP and qBittorrent downloads are marked complete or sent through post-processing. Corrupt videos are visibly marked as re-downloading after a failed video check, replaced automatically once, logged at each decision, and left failed with the complete ffprobe reason if the replacement also fails.
- Added a Jellyfin server plugin and JAVBeacon integration API for automatic
  path/code matching with container path remaps, manual Identify search,
  metadata and artwork, persistent JAVBeacon/Stash provider IDs, and
  configurable per-user playback tracking.
- JAVBeacon now owns durable playback checkpoints, resume/play-duration and
  completion handling, Stash play/O mutations, activity lookup, and the
  optional Jellyfin Web activity and **+1 O** panel, keeping Jellyfin isolated
  from Stash credentials and scene-mapping logic.
- The Jellyfin plugin can now maintain a configurable native Watchlist
  collection from StashApp's configured Watchlist tag, including removals,
  and queue library scans after realtime or scheduled Stash scene changes.

### Changed

- Retargeted the Jellyfin plugin and packaged release build to Jellyfin 12.0.0
  and .NET 10. The release workflow now publishes a ready-to-install Jellyfin
  12 plugin archive alongside the JAVBeacon binaries.

## [1.0.133] - 2026-09-08

### Added

- Realtime StashApp synchronization can now receive authenticated scene-create,
  scene-update, and scene-delete hooks, coalesce rapid duplicate events, retry
  transient failures, and update only the affected JAVBeacon release and its
  playback history. A ready-to-install Stash plugin is included under
  `contrib/stash-javbeacon-realtime`.
- Settings → StashApp now includes controls for the dedicated webhook secret,
  debounce period, retry count, and retry delay. The scheduled full sync remains
  available as a reconciliation fallback.

## [1.0.132] - 2026-09-08

### Added

- Release Details now shows a compact watched indicator when StashApp history
  exists. It opens a focused modal with exact play and orgasm dates, per-play
  duration, summary totals, and a small recent-activity graph without adding
  vertical bulk to the main release view.
- Orgasm activity uses a dedicated minimal white three-droplet glyph throughout
  the new detail modal, paired with the existing pink orgasm graph styling.

## [1.0.131] - 2026-09-08

### Changed

- Download Monitoring now places **HTTP** before **Torrent**, uses the clearer
  Torrent label instead of qBittorrent, and offers page sizes of 250 and 500.
- The header activity center’s release rows now use a compact, readable layout
  with cleanly separated identity, queue state, source, and transport details.

### Fixed

- Stale download-history rows whose external torrent was already removed can
  now be deleted locally without contacting qBittorrent again. Bulk deletion is
  also idempotent when a selected terminal row disappears during auto-refresh,
  instead of failing with “no matching downloads selected”.

## [1.0.130] - 2026-09-08

### Changed

- The top-right status widget is now a larger, persistent activity center with
  live searching, waiting, ready, and downloading counts; queue advancement;
  and richer per-release source, transport, and status details. Its accidental
  one-click dismiss control has been removed.

### Fixed

- Search-and-download work now reports the effective HTTP or torrent transport
  from the global download method and per-release override consistently. Newly
  queued, promoted, already queued, and restart-resumed work is corrected, so
  HTTP-first jobs no longer appear under qBittorrent or leave stale In Progress
  counts behind.

## [1.0.129] - 2026-09-08

### Fixed

- Stash history write-back now handles duplicate release codes deterministically
  by disambiguating matching scenes with their case-insensitive filenames. This
  prevents an existing event from being offered again when another Stash scene
  incorrectly shares the same release code.
- Review and final write-back validation now stop safely if a Stash history
  timestamp cannot be parsed, instead of silently treating that existing event
  as absent and potentially creating a duplicate.

## [1.0.128] - 2026-09-08

### Changed

- Stash History’s write-back action is now named **Review & Sync**.

### Fixed

- History write-back reviews now retrieve the complete Stash scene library in
  deterministic pages instead of relying on an unlimited single response.
  Reported totals, server-side page caps, duplicate scene IDs, repeated pages,
  and a pagination safety ceiling are handled so successive reviews remain
  complete and stable on large libraries.

## [1.0.127] - 2026-09-08

### Changed

- Stash History now places its filtered history details beside the activity
  graph in a viewport-sized workspace. The details rail scrolls independently,
  includes a persistent cover-size control, and uses smaller favicon-led source
  buttons so the complete view fits without scrolling the entire page.

## [1.0.126] - 2026-09-08

### Fixed

- GIGA scraping now rebuilds its Akiba session and retries the requested page
  once when Akiba returns an unrelated HTTP-200 page. This prevents active
  releases such as `GHMT-36` from being misreported as invalid after a stale or
  transiently confused site session.

## [1.0.125] - 2026-09-08

### Fixed

- GIGA scraping now derives its primary online page estimate from Akiba’s
  displayed title count at 20 releases per page. For example, `4385 Titles`
  produces a 220-page estimate while empty and repeated pages remain early-stop
  safeguards.

## [1.0.124] - 2026-09-08

### Fixed

- GIGA scraping no longer mistakes Akiba’s sliding window of visible page
  numbers for the site’s final page. Limited runs now honor their configured
  page count, while all-page runs continue until an empty or repeated page
  identifies the real end.

## [1.0.123] - 2026-09-08

### Added

- Release Library structured search can now filter releases by their associated
  Monitoring Site. Condition fields are alphabetized and the filter workspace
  provides more room for long field names and values.

### Fixed

- GIGA releases now migrate legacy Akiba product URLs to the current path,
  retry broken detail links through an exact release-ID lookup, and consistently
  use `GIGA` as the studio for both new and existing GIGA monitoring records.
- Download Activity background refreshes no longer flash a temporary
  “Refreshing” label or disabled refresh-button state, and overlapping automatic
  refresh requests are suppressed.

## [1.0.122] - 2026-09-08

### Added

- Failed HTTP and qBittorrent rows now show the date and time the failure was
  recorded. Other activity states continue to show their original added time.

## [1.0.121] - 2026-09-08

### Fixed

- Download Activity filter wording is now transport-aware: HTTP searches refer
  to HTTP filenames, while qBittorrent searches continue to refer to torrent
  names. The placeholder updates immediately when switching transport tabs.

## [1.0.120] - 2026-09-08

### Fixed

- Updated the embedded-frontend release assertion for the new persistent
  **In Progress** download state, allowing the release pipeline to validate
  and package the frontend changes introduced in v1.0.119.

## [1.0.119] - 2026-09-08

### Added

- Download Monitoring now has a persistent **In Progress** tab for provider
  searches, ordered with the active search first and waiting searches in queue
  order. Every activity tab now includes a live item counter.
- Search + Download work is stored durably. Queued searches and active HTTP
  downloads recover after a JAVBeacon restart; interrupted partial HTTP files
  are discarded and restarted cleanly from zero.
- The Stash history write-back review is now a wide, tabbed workspace for
  Changes, Matched, and Unmatched records, with covers, release and Stash scene
  links, partial/wildcard filters for release ID, path, and filename, individual
  and bulk selection, sync reasons, and expandable exact proposed changes.

### Changed

- Selecting **Monitor + download** for an already monitored release now treats
  the immediate run as a forced download while preserving its monitoring state.
- Download activity auto-refresh now updates existing rows in place instead of
  rebuilding their covers and controls every two seconds, eliminating flicker.
- HTTP transfer size, current speed, and the recent-speed graph now have a
  clearer non-overlapping layout.

### Fixed

- Stash history write-back now re-fetches current Stash activity immediately
  before applying changes and deduplicates play and orgasm timestamps at the
  precision Stash accepts. Already-synchronized events are excluded from the
  Changes tab, multiple archived records targeting one scene cannot increment
  the same event twice, and playtime deltas are recalculated before writing.

## [1.0.118] - 2026-09-08

### Added

- Release Library structured-search conditions can now be inverted with an
  **Exclude** option. Inversion works across text, exact/wildcard metadata,
  numeric/date comparisons, boolean fields, AND/OR groups, and saved presets.
- Added a Gluetun container update helper with dry-run and remote-host support.
  It pulls fresh images, preserves live dependent-container configuration, and
  reattaches containers that share each updated Gluetun network namespace.

## [1.0.117] - 2026-09-08

### Added

- Settings now has a dedicated Notifications tab for PikPak account health,
  every configured Byparr instance, and Download + Search failure monitoring.
  One shared Pushover user/group key is combined with a separate application
  token and test-notification action for each category.
- Byparr health checks track each enabled instance independently, alert once
  after a configurable number of consecutive failures, optionally report
  recovery, and re-arm only after the affected instance is healthy again.
- Download, HTTP search, and scraping failures now use configurable category
  weights, score threshold, rolling window, and cooldown. JavLibrary failures
  are included, while repeated Cloudflare/Byparr attempts for the same release
  or URL are collapsed into one incident to prevent notification floods.

## [1.0.116] - 2026-09-08

### Added

- Settings → Scraping now groups anti-bot measures and provides optional,
  fully configurable Gluetun recovery for direct JavDB HTTP 403 responses,
  including the control-server URL, optional API key, rotation count,
  reconnect timeout, polling cadence, settle delay, IP-change requirement,
  connection test, and Docker Compose/authentication examples.
- JavDB can now rotate its Gluetun VPN connection until a different public IP
  is verified, retry the direct request once, and then fall back to the
  existing priority-aware multi-instance Byparr pool. Same-IP reconnects try
  at least three rotations, and concurrent 403s share one rotation lock.

## [1.0.115] - 2026-09-08

### Fixed

- JavDB HTTP-provider searches now retry search, release-detail, and download
  action pages through the configured priority-aware multi-instance Byparr /
  FlareSolverr pool only when the direct JavDB request returns HTTP 403.
  Failed solver instances fall through to the next available instance while
  retaining the shared cooldown and concurrency controls; Keepshare and PikPak
  traffic remains on its existing API and redirect paths.

## [1.0.114] - 2026-09-08

### Added

- Release Library structured search now includes a StashApp video file path
  condition. It matches partial paths case-insensitively by default, supports
  exact and wildcard matching, and filters the complete server-side result set
  across pagination and saved condition presets.

## [1.0.113] - 2026-09-08

### Changed

- PikPak restores now follow the provider's restore task and validate any
  returned destination file ID directly before scanning account folders.
  Source/trace IDs are rejected unless their fetched filename and byte size
  match the selected video, and the verified destination parent folder ID is
  stored with Download Activity for reliable cleanup and diagnostics.

## [1.0.112] - 2026-09-08

### Fixed

- Authenticated PikPak downloads now locate restored files incrementally and
  prioritize PikPak's `Pack From Shared` folder instead of repeatedly walking
  the user's entire drive. Large accounts no longer time out after a restore
  that visibly succeeded, and any drive-list failure is retained in the final
  error details.

## [1.0.111] - 2026-09-08

### Fixed

- PikPak saved-session refreshes no longer send the public web client's
  embedded client secret, which PikPak now rejects with HTTP 403. Downloads
  can refresh a valid saved account session without unnecessarily falling
  back to a fresh credential sign-in.

## [1.0.110] - 2026-09-06

### Added

- Downloads settings now support a row-based filename blacklist. Partial
  matches are case-insensitive and are hard exclusions across Torrent and HTTP
  searches, including relaxed non-preferred and manual force-download paths.

## [1.0.109] - 2026-09-06

### Changed

- Release Library and Monitored Releases bulk Search + Download submissions
  now join a FIFO job queue when another bulk job is active instead of being
  rejected. The UI reports the new job's queue position.

## [1.0.108] - 2026-09-06

### Added

- Stash History detail items now include larger, visually distinct branded
  buttons for JAVBeacon release details, JavLibrary, and the matching StashApp
  scene. Source buttons use site icons and no longer use a trailing arrow.
- Release Library bulk selection now offers **Select all matching** after the
  first item is selected. It selects the complete server-side filtered result
  set rather than only cards already loaded by infinite scrolling.

## [1.0.107] - 2026-09-06

### Changed

- JavDB/Keepshare HTTP discovery now relies on strict canonical release-ID
  matching and no longer rejects an exact match because its JavDB release date
  differs from the date stored in JAVBeacon.
- Reworked Stash History into calendar-scoped Daily, Weekly, Monthly, and
  Yearly views with previous/current/next navigation. Graph selections now
  drill into the next level and filter the detail ledger to the same range;
  Watch and Stash Orgasm charts use distinct blue and pink palettes.
- Stash History now opens in Daily view by default, remembers the selected
  period, fetches only the active calendar range, and incrementally loads its
  detail ledger through infinite scrolling instead of sending every stored
  watch record to the browser.

## [1.0.106] - 2026-09-06

### Fixed

- Completed Web downloads now execute both ordered post-processing event
  stages consecutively, including finalization/removal steps that previously
  ran only after qBittorrent removal. The Settings help now documents the
  equivalent HTTP lifecycle explicitly.

## [1.0.105] - 2026-09-06

### Added

- Download Monitoring now has a separate Queued tab for downloads waiting on
  the configured parallel HTTP-download limit.

### Changed

- Waiting HTTP downloads now use an explicit first-in, first-out slot queue.
  They remain durably queued until capacity is available, then move to
  Downloading in queue order; restart recovery restores that ordering.
- Replaced every native browser confirmation and text prompt with consistent
  JAVBeacon modal dialogs, including active HTTP cancellation, bulk actions,
  filter naming, site deletion, and maintenance jobs.
- Removing a Web download now also deletes the exact temporary account-drive
  copy JAVBeacon restored in PikPak. Ownership and file identity are persisted
  so cleanup remains safe after failures, cancellation, or an app restart.

## [1.0.104] - 2026-09-06

### Changed

- Split the header Search + Download status menu into clearly labeled
  searching and downloading groups. Opening an active download now keeps the
  full Download Activity overview intact and highlights the matching row
  instead of applying a release-ID search filter.
- qBittorrent status polling now uses a 15-second minimum/default interval and
  suppresses repeated identical connection errors until polling recovers.

### Fixed

- PikPak's 40-character resource `hash` is no longer treated as the SHA-1 of
  downloaded file contents. Completed Web downloads retain strict remote byte
  size verification and use only PikPak's explicit `md5_checksum` when it is
  present, preventing valid files from being deleted as checksum mismatches.
- Parallel Web downloads now validate each range response's byte count and cap
  every response body at its requested boundary, preventing a non-conforming
  CDN response from overwriting adjacent segments.

### Added

- Download Activity now shows a prominent Clear filters action whenever one
  or more advanced download filters are active.
- Added a durable Stash History section with separate watch and “Stash Orgasm
  history” timelines, same-day event merging, linked covers and Release
  Details, retained JSON export, and Daily/Weekly/Monthly/Yearly drill-down
  graphs. Stash exposes only scene-wide play duration, so JAVBeacon marks
  per-day duration allocations as estimated while preserving the exact total.
- Added review-first Stash history write-back using JavLibrary URL, release ID,
  then case-insensitive filename matching. Exact play/O timestamps and missing
  play-duration totals are shown for review before manual confirmation;
  optional scheduled write-back is disabled by default.

## [1.0.103] - 2026-09-06

### Added

- Added an optional Download setting that makes Search + Download actions on
  release covers and Release Details run automatic provider selection in the
  background without opening the interactive results window.
- Expanded the header status widget with the live Search + Download queue.
  Its dropdown lists active release IDs and links directly to each release in
  Download Activity's Downloading view.
- Added an opt-in PikPak fallback for shares whose exact release-ID folder
  contains generically named video files. It honors preferred-filename
  priority first and file size second, while remaining disabled by default.

### Fixed

- Keepshare candidates whose PikPak contents cannot be inspected are now
  diagnostic-only results and can no longer queue a placeholder folder that
  is guaranteed to fail during download resolution.

## [1.0.102] - 2026-09-06

### Added

- Completed Web downloads now verify the final on-disk byte size and compare
  the local file against PikPak's SHA-1 hash (or MD5 checksum when that is the
  available digest) before the temporary file is promoted to the final MP4.
  Providers that omit a digest retain strict byte-size and range validation.

## [1.0.101] - 2026-09-06

### Changed

- Connections per HTTP download is now capped at four, matching PikPak's
  reliable concurrency level and preventing unsupported higher values from
  overwhelming the media gateway.
- Parallel Web transfers automatically reduce from four connections to two
  and finally one after gateway or range-worker failures, with progressive
  retry delays and a 45-second no-progress watchdog for stalled response
  bodies.
- The final single-connection fallback retries interrupted response bodies and
  resumes from the last confirmed byte when PikPak continues to support range
  requests.
- Transient PikPak restore failures are reconciled against the account before
  up to three safe restore attempts, avoiding duplicate files after ambiguous
  gateway responses. A transfer that exhausts every connection level refreshes
  its authenticated signed URL once and repeats the fallback chain.

### Fixed

- PikPak `502` responses and indefinitely stalled range streams no longer
  strand an HTTP download; JAVBeacon resets the partial layout and retries at
  the next safe connection level, retaining the existing single-stream path as
  the final fallback.
- HTTP logs now distinguish newly restored PikPak files from deduplicated
  account files and record every automatic connection downgrade with its
  underlying failure.

## [1.0.100] - 2026-09-06

### Added

- HTTP Downloads now has a separate **Connections per HTTP download** setting,
  defaulting to four, for accelerating an individual PikPak transfer without
  changing how many releases may download simultaneously.

### Changed

- Large HTTP files are downloaded through validated parallel byte ranges when
  supported, with combined progress, speed, and ETA reporting. Interrupted
  segments resume independently, while providers that ignore range requests
  automatically retain the safe single-stream download path.
- Every segment is checked against the selected file's exact total size and
  expected byte range before it is written to the shared temporary file.

## [1.0.99] - 2026-09-06

### Fixed

- Authenticated PikPak restores no longer mistake the shared source trace ID for
  the new account-owned file ID, which caused restored Web downloads to fail
  with `file_not_found`.
- JAVBeacon now inventories the PikPak account around a restore, waits for the
  exact filename and byte size selected during search, safely reuses an exact
  copy when PikPak deduplicates the restore, and only removes files created by
  the current restore when cleanup is enabled.

## [1.0.98] - 2026-09-06

### Added

- PikPak account sessions are now persisted with their device identity,
  renewable refresh token, issue time, and provider-reported expiry so
  authenticated Web downloads no longer require a fresh login every time.
- Settings now shows the recorded PikPak session lifetime and exposes PikPak's
  official human-verification link when the provider requires an interactive
  CAPTCHA challenge.
- Failed automatic PikPak re-authentication can send a one-time Pushover alert
  through the existing failed-validation notification option; another alert is
  allowed only after authentication recovers and subsequently fails again.

### Changed

- Authenticated downloads and scheduled account checks first rotate the saved
  refresh token, then automatically fall back to the configured credentials if
  PikPak rejects that session.
- Stored PikPak access tokens, refresh tokens, user/device identifiers, and
  notification state remain backend-only and are omitted from settings API
  responses.

### Fixed

- PikPak sign-in CAPTCHA initialization now includes the account name expected
  by the provider, while the sign-in request supplies the issued CAPTCHA token
  in both its body and header.
- PikPak authentication failures redact credentials even when the provider
  echoes an account name or password in its error description.

## [1.0.97] - 2026-09-06

### Added

- Settings → Downloads now supports an optional PikPak account for full-size
  JavDB/Keepshare downloads when an anonymous public share authorizes only a
  partial preview stream.
- Added **Test & re-authenticate**, which performs a fresh PikPak sign-in and
  authenticated drive-access check and displays the persisted result and
  timestamp in the settings interface.
- Added a configurable PikPak account-validation schedule, included in the
  existing schedule forecast, with independent Pushover notifications for
  successful and failed checks.
- Added optional cleanup that permanently removes only the exact file restored
  into PikPak after JAVBeacon has successfully written the complete local file.

### Changed

- Authenticated HTTP downloads restore only the exact search-selected PikPak
  file and prefer its explicitly identified original-quality media URL.
- PikPak and Pushover credentials are excluded from account-check results,
  application log fields, and Pushover message bodies.

### Fixed

- HTTP downloads now distinguish an anonymous partial-preview response from an
  authenticated account storage, transfer-quota, or restore limitation instead
  of reporting either as a generic wrong-filesize failure.
- Restored PikPak files are revalidated against the selected release ID,
  filename, and filesize before a download is allowed to start.

## [1.0.96] - 2026-09-06

### Changed

- HTTP result cards in Search & Download now use the shorter `Web` transport
  label while retaining `Web download` for the action itself.
- Search-result cards now share consistent title typography, header spacing,
  state placement, metadata blocks, and action alignment across Web and
  Torrent results.

### Fixed

- The selected first-choice result no longer inserts a second independent
  header badge that distorts the card, shifts its content downward, or makes
  it visually inconsistent with adjacent results.
- Transport badges remain on the left and the single compact result-state or
  priority badge remains on the right at three- and four-column card widths.

## [1.0.95] - 2026-09-06

### Added

- Preferred filename patterns are now managed as individual settings rows with
  an explicit numeric priority. Priority 1 is highest; existing newline-based
  patterns are migrated automatically to priority 10.
- Torrent and HTTP search results now expose the matched filename priority, and
  Download Activity shows a compact live speed graph for active HTTP transfers.

### Changed

- Torrent and HTTP candidate selection now chooses matches from the
  highest-priority configured filename pattern before comparing seed counts or
  file sizes. Equal-priority Torrent matches still prefer more seeds, while
  equal-priority HTTP matches still prefer the largest file.
- Download Activity uses a denser active-transfer layout with smaller transfer
  metrics and continuously updated HTTP speed history.

### Fixed

- Manual HTTP selection now pins the exact PikPak file ID, filename, and size
  discovered during search, preventing download-time resolution from silently
  substituting another release-ID-matching file from the same share.
- Removing an active HTTP download now cancels its worker and clears its HTTP
  history without attempting to contact qBittorrent.
- The new preferred-pattern list no longer leaves the obsolete textarea taking
  up empty space beneath its row controls.

## [1.0.94] - 2026-09-05

### Changed

- Manual HTTP search now keeps an exact, date-compatible JavDB metadata match
  visible when the release has no published Keepshare/PikPak download link,
  linking to its JavDB page and clearly marking it as non-downloadable.
- Manual, monitored bulk, and scheduled HTTP searches now write the complete
  provider stage, source page, normalized release ID, and failure reason to
  the application log.

### Fixed

- Exact JavDB matches without a downloadable share are no longer hidden behind
  a generic `no exact, date-compatible result` message.
- Monitored bulk search now preserves the provider's specific unavailable-share
  reason instead of replacing it with a generic no-candidate summary.

## [1.0.93] - 2026-09-05

### Changed

- JavDB HTTP searches now report their exact failure stage, distinguishing
  blocked requests, unparseable search results, ID mismatches, date mismatches,
  detail-page failures, missing download structures or links, and Keepshare
  inspection failures in manual, monitored, and automatic searches.
- JavDB download discovery now tolerates alternate wrapper markup, follows
  separate Download actions, recognizes Keepshare redirect hosts and direct
  PikPak shares, and removes duplicate share links.

### Fixed

- JavDB release IDs are now matched case-insensitively and independently of
  spaces, dashes, underscores, and dots while retaining strict boundaries that
  reject longer or otherwise distinct IDs.
- Exact JavDB releases such as `PRED-899` and `PRPM-002` now progress through
  detail/download discovery instead of silently becoming a generic no-result
  response when a later provider stage fails.
- Generic PikPak help links are no longer mistaken for downloadable shares,
  and normal JavDB pages containing Cloudflare's footer script are no longer
  misclassified as challenge pages.

## [1.0.92] - 2026-09-05

### Added

- Release Details opened from Monitored Releases now supports Previous and
  Next navigation across the complete filtered and sorted monitored list,
  automatically loading adjacent result pages in either direction.

### Changed

- Monitored bulk Search + Download logs now distinguish downloads that were
  queued, releases for which no provider candidate was found, genuine policy
  or active-download skips, and failures, with the exact reason recorded for
  every affected release.

### Fixed

- Forced Monitored Releases actions now apply the selected transport,
  preferred-filename, StashApp, and download-history overrides directly to the
  immediate background run as well as persisting them for later searches.
- Provider misses are no longer misleadingly reported as skipped forced
  downloads; `skipped` is reserved for an actual duplicate or policy skip.

## [1.0.91] - 2026-09-05

### Fixed

- Keepshare inspection now follows intermediate redirects such as
  `keepshare.org` to `keepshare.cc` before stopping at the PikPak player URL.
  This restores the complete file list, filenames, file sizes, matched-file
  details, and preferred filename detection without requesting the player page
  that previously timed out.

## [1.0.90] - 2026-09-05

### Added

- Every entry in Live Server Logs now has a Copy action that includes its
  timestamp, level, complete message, and all structured fields.
- Live Server Logs can export the currently loaded entries matching the active
  level and text filters as a timestamped plain-text log file.

### Changed

- Retrying a failed HTTP download now reuses and immediately re-queues the
  failed activity row, clears its stale progress and error state, and records
  queued, started, resumed, completed, and failed lifecycle events in the
  server log.
- Keepshare/PikPak resolution now retries transient failures and extracts the
  public PikPak share identifier directly from redirects or direct share URLs,
  avoiding a blocking request to the playback page before resolving the
  original-quality stream.

### Fixed

- Failed HTTP activity entries can now be deleted individually or in bulk
  without contacting qBittorrent; only the selected failed history rows are
  removed, leaving unrelated Torrent and HTTP history intact.
- Retrying a failed HTTP transfer no longer immediately returns to Failed
  without starting a new worker, while an active HTTP transfer for the same
  release is still protected from duplication.
- Long Download Activity failure details now remain aligned below the release
  information instead of overlapping the cover and table columns.

## [1.0.89] - 2026-09-05

### Added

- Failed rows in Download Activity now show the complete, wrapped failure
  reason instead of hiding it in a tiny truncated progress label.
- Download failures now emit structured server-log entries with the download
  and release IDs, transport, provider, source, match context, provider
  response, fallback state, and full error detail.

### Fixed

- Direct HTTP video downloads now retry temporary 429, 500, 502, 503, and 504
  upstream responses three times before failing, while preserving the required
  stream headers and reporting the upstream response detail after exhaustion.
- Keepshare/PikPak media streams remain direct downloads instead of being
  incorrectly proxied through Byparr, which is reserved for browser challenges
  on HTML pages.

## [1.0.88] - 2026-09-05

### Changed

- Compacted the Monitored Releases selection bar so Stop monitoring, Force
  download, and Clear selection share the same height, padding, typography,
  corner radius, and icon scale.
- Shortened the primary bulk-action label and tightened the surrounding
  spacing so the controls remain readable without dominating the results.

## [1.0.87] - 2026-09-05

### Added

- Monitored Releases now supports selecting multiple releases and launching a
  forced Search + Download batch with an explicit Torrent-only or HTTP-only
  transport.
- The same bulk action can apply Allow non-preferred filenames, Ignore
  StashApp, and the new independent Ignore download history override to every
  selected release at once.

### Changed

- Per-release Torrent/HTTP choices are saved and also govern later scheduled
  searches, while active downloads using the chosen transport remain protected
  from duplication.
- Ignore StashApp and Ignore download history are now separate controls, so a
  missing local file can be bypassed without automatically discarding valid
  JAVBeacon download history.
- Monitoring rows expose strict transport and history overrides, and download
  activity/log reasons identify when a release-specific method selected the
  transport.

### Fixed

- Switching to HTTP Download Activity no longer briefly renders stale
  qBittorrent rows while the HTTP request is loading.

## [1.0.86] - 2026-09-05

### Added

- Added a persistent Default Download Method setting with Torrent to HTTP
  fallback, HTTP to Torrent fallback, Torrent-only, and HTTP-only strategies.
- Added an optional HTTP priority rule when Torrent and HTTP both match the
  same preferred filename and their known matched-file sizes are within 10%.
- Search results now highlight the result selected by the configured download
  strategy, while Download Activity and server logs explain transport choices
  and fallback reasons.

### Changed

- Manual Search & Download, Missing Files recovery, scheduled monitored
  searches, stalled Torrent handling, and failed HTTP transfers now use the
  same configured transport strategy.
- Torrent-only and HTTP-only modes strictly avoid contacting or falling back
  to the other transport.
- Removed the obsolete per-release HTTP-primary control and restored Release
  Details Status to a compact two-column Download and StashApp layout without
  an empty slot.

## [1.0.85] - 2026-09-05

### Added

- HTTP Download Activity now reports the live transfer rate alongside
  transferred size and ETA.

### Changed

- Release cards and Release Details now retain and visibly distinguish the
  linked download transport; HTTP status links open the exact Keepshare source
  and show HTTP-specific telemetry.
- Force Download now ignores completed or historical download records and
  active downloads using the other transport, while preserving any queued,
  downloading, or processing job using the same transport.
- Manual and scheduled force-redownload paths now share the same
  transport-aware duplicate protection.

### Fixed

- HTTP releases no longer inherit Torrent-specific status presentation or
  seeds and peers telemetry.
- Historical download rows no longer prevent an explicitly requested Force
  Download.

## [1.0.84] - 2026-09-05

### Changed

- Search & Download now fits three to four compact result cards in its existing
  dialog footprint and gives Torrent and Web downloads distinct icons and
  action labels.
- Manual searches now offer an explicit force-redownload action for releases
  already present in StashApp, while ordinary manual downloads retain duplicate
  protection.
- Mixed Torrent and HTTP results wait for both providers before presenting the
  final preferred ordering, preventing late HTTP results from shifting cards.

### Fixed

- Empty HTTP search results are consistently returned as an empty array and the
  interface defensively handles malformed provider result payloads, preventing
  the `Symbol.iterator` search failure.
- Direct HTTP downloads no longer contact or depend on qBittorrent, so they can
  be queued while the torrent client is unavailable.
- Torrent and HTTP contents are ordered by file size from largest to smallest,
  with unknown sizes last and deterministic filename ordering for ties.

## [1.0.83] - 2026-09-05

### Added

- Failed HTTP downloads can now be retried in bulk, either for the selected
  rows or for every failed HTTP download across all pages.

### Changed

- Standardized action icons and alignment across Download Activity, Monitored
  Releases, Missing Files Active Tasks, refresh controls, and Release Details.
- Moved the per-release HTTP primary/fallback override into the Status block so
  it no longer wraps into a clipped fourth Tracking row.
- Bounded the Search & Download dialog to a practical desktop footprint while
  retaining its larger, readable result cards.
- Torrent and HTTP provider leaders are now paired first, with preferred
  filename ranking applied before every incremental render and a reserved HTTP
  result slot that prevents late provider results from reshuffling the grid.

## [1.0.82] - 2026-09-05

### Changed

- Enlarged the Search & Download dialog, provider progress, result cards,
  filenames, file lists, match indicators, and actions for substantially
  better readability while retaining the desktop two-column layout.

### Fixed

- HTTP search now inspects Keepshare candidates in a stable order and retries
  transient anonymous-share failures once, preventing a preferred filename
  from being discovered only after its download has already started.
- Keepshare inspection failures are now reported on their search result rather
  than silently presenting an incomplete filename match.

## [1.0.81] - 2026-09-05

### Added

- Search now reports Torrent/Nyaa and HTTP/JavDB → Keepshare as independent
  provider stages, showing live searching, completion counts, and provider
  failures while results appear as each provider finishes.
- Torrent and HTTP search results now expose structured file contents with a
  checked matched-file indicator and per-file sizes whenever the provider
  supplies them.

### Changed

- Preferred filename results are visually distinguished with a dedicated
  badge, accent border, and background treatment so the preferred choice is
  immediately apparent in mixed Torrent and HTTP results.
- Search result file lists use a denser, cleaner layout with a separate matched
  file summary and expandable overflow for larger contents lists.

## [1.0.80] - 2026-09-05

### Changed

- HTTP discovery now evaluates every distinct Keepshare link on a matching
  JavDB release and inspects the actual downloadable filenames before ranking
  candidates. Files matching the configured preferred filename patterns take
  priority, followed by the existing non-`-U` and largest-file ordering.
- Renamed the user-facing "Accepted filename patterns" setting to "Preferred
  filename patterns" to describe its ranking role more accurately while
  retaining existing settings and behavior.
- Manual search results and HTTP Download Activity now expose separate links
  to both the originating JavDB release and its Keepshare download page.

### Fixed

- SQLite and PostgreSQL downloads now retain both HTTP source links across
  queueing, progress updates, failures, and retries, with a compatible
  migration for existing databases.

## [1.0.79] - 2026-09-05

### Added

- Added a modular direct-HTTP download provider system with an initial JavDB,
  Keepshare, and anonymous PikPak implementation. It matches release IDs
  case-insensitively with optional separators, rejects partial matches, checks
  release dates within 60 days, prefers the largest non-`-U` result, and
  resolves the highest-quality original playable file without requiring a
  PikPak account.
- Added an HTTP Download Activity area with separate downloading, completed,
  and failed views, live byte progress and ETA, configurable parallelism,
  persistent destination paths, automatic collision-safe filenames, and retry
  controls.
- Releases can persist HTTP as their primary download provider while keeping
  Torrent as the default primary provider.

### Changed

- Manual Search and Search + Download now query Nyaa Torrent and JavDB HTTP
  together and present both in one combined result list with prominent
  transport labels, provider details, file sizes, swarm health, and appropriate
  download actions.
- Immediate, bulk, and scheduled monitored searches now share the same provider
  selection logic. Torrent-first searches fall back to HTTP when lookup fails,
  no acceptable or seeded result exists, or qBittorrent submission fails.
- Active torrents can switch automatically to HTTP when they are stalled and
  not progressing with no seeders or no recorded completed peer. The safety
  delay is persistent and configurable under Downloads → HTTP, defaults to
  eight hours, preserves the torrent if HTTP discovery fails, and retries the
  lookup after a cooldown.

### Fixed

- Added compatible SQLite and PostgreSQL columns and indexes for transport,
  HTTP progress, destination paths, and per-release provider preference so
  upgrades retain existing Torrent download history safely.

## [1.0.78] - 2026-09-05

### Fixed

- Release Details Story text now uses the same readable font sizing and text
  color as the surrounding release information, including the compact-height
  layout.

## [1.0.77] - 2026-09-05

### Changed

- JavLibrary scans now exclude releases whose studio is GIGA, using a
  case-insensitive studio match, so GIGA metadata comes exclusively from the
  dedicated Akiba-web/GIGA scraper. A one-time SQLite and PostgreSQL migration
  removes existing JavLibrary-origin GIGA duplicates without touching releases
  collected by the dedicated provider.

### Fixed

- Release Details now uses the available Story area instead of clipping text
  to one or two lines when the panel has room to display it.

## [1.0.76] - 2026-09-05

### Added

- Missing Library Files conditions can now filter on the date of the latest
  StashApp O-count event. Missing scans persist the newest `o_history` value
  in both SQLite and PostgreSQL, with an automatic compatibility fallback for
  older StashApp schemas that do not expose O-history.
- Missing Library Files now has its own persistent saved-filter presets,
  separate from Release Library presets. A preset retains the selected state,
  structured conditions, sort field and direction, and page size.

### Changed

- Missing Library Files states are grouped into compact Scraping and Download
  families with contextual second-row state tabs, while All, Missing, and
  Active Tasks remain dedicated tabs. Every tab includes a detailed hover
  explanation of the records and workflow state it displays.

## [1.0.75] - 2026-09-05

### Changed

- Release Library selection controls no longer permanently consume the
  cover's status-pill space. A card reveals its checkbox on hover or keyboard
  focus; after the first release is selected, every loaded card keeps its
  checkbox visible and cover clicks toggle additional selections. The mode
  remains active as more releases are appended while scrolling.
- Shift-clicking a release checkbox or cover now selects the complete loaded
  range between the first selected release and the clicked release, making
  large contiguous Monitor + Download batches much faster to assemble.
- Missing Library Files now presents folder scoping explicitly as a multi-row
  list in both the pre-scan dialog and StashApp settings. Every non-empty row
  is an independent case-insensitive scope and a scene is included when its
  path matches any row; existing newline-separated settings remain compatible.

## [1.0.74] - 2026-09-05

### Added

- Missing Library Files Search + Download batches now run without a blocking
  progress dialog. A highlighted Active tasks tab shows the remaining count,
  live per-release stages, successful torrent matches, and exact lookup or
  download failures, while retaining provider, match, size, seeds, peers, ETA,
  last-seen-complete, and torrent-source details for each result.
- Active tasks can be filtered to active, successful, or failed states.
  Failed and not-found searches can be retried individually or together,
  while preserving the original non-preferred-filename choice.

## [1.0.73] - 2026-09-05

### Added

- Release Library cards now support multi-selection across incrementally
  loaded pages. Selected releases can be marked as monitored and sent through
  Search + Download immediately from one compact bulk dialog, with optional
  per-release overrides for StashApp-local detection and non-preferred
  filenames. The background work is processed sequentially to avoid flooding
  the configured search provider or download client.

## [1.0.72] - 2026-09-05

### Fixed

- PostgreSQL upgrades now add the new StashApp video-path column before
  creating its case-insensitive lookup index, preventing an existing
  installation from entering a restart loop with a missing-column error.

## [1.0.71] - 2026-09-05

### Added

- StashApp local-library sync now stores the complete video file path for
  each matched release and exposes it as `stash_file_path` in release API
  responses. API clients such as JAVBeaconSubs can resolve a file directly
  through the new exact, case-insensitive `stash_file_path` filter. Indexed
  SQLite and PostgreSQL migrations keep these lookups fast, and stale paths
  are cleared when a scene is no longer local.

## [1.0.70] - 2026-09-05

### Added

- Monitored releases can now be flagged "Ignore StashApp Local (force
  download)" - a new persistent per-release override, alongside the
  existing "Allow non-preferred filenames" flag, in the Download
  Monitoring → Monitored releases tab (filter checkbox and bulk on/off
  actions). A release with this flag set is downloaded by the scheduled
  search job (and any manual search) even though it already has a matched
  StashApp scene, instead of being skipped as an "already exists" duplicate.
- Missing Library Files now sets that new flag automatically, on every
  release it marks monitored (both "Set Monitored Only" and "Set Monitored
  + Download + Search"): a release recovered there already has a StashApp
  scene by definition - only its file on disk is missing - so it must never
  be skipped as a duplicate the way an ordinary already-linked release
  would be.

### Fixed

- Akiba/GIGA release details now read the complete story from the hidden
  expanded story block instead of storing the shortened preview and its
  “More” control. The scraper also excludes the expanded block's “Close”
  control, with a clean fallback for pages that only provide a short story.

## [1.0.69] - 2026-09-02

### Added

- The API key (`JAVBEACON_API_KEY`) is now configurable from Settings →
  General → API access, with Copy and Regenerate buttons, and takes effect
  immediately without a restart. A fresh install generates a random key
  automatically the first time it starts; an existing install's
  `JAVBEACON_API_KEY` environment variable still seeds the initial key on
  upgrade. Clearing the field and saving disables API-key access entirely.
- `GET /api/releases` (and `/api/releases/count`) now accept a `video_id`
  query parameter for an exact, case-insensitive release lookup, and the
  existing free-text `search` parameter now also matches a release's stored
  scraper ID and product URL. Both support the community StashApp JavLibrary
  scraper's new optional JAVBeacon-backed lookup mode, which resolves a
  scene directly from JAVBeacon's own already-scraped release library
  instead of re-scraping JavLibrary through Byparr/Cloudflare.

### Fixed

- Release Details no longer shows a stale native tooltip alongside its
  redesigned Downloading/Downloaded and StashApp status badges. Downloading
  had kept its old "A torrent for this release is currently downloading"
  mouseover text underneath the newer hover telemetry card (ETA, seeds/
  peers, last seen complete), and Downloaded/StashApp had kept an old
  "Downloaded on .../Added to StashApp on ..." mouseover repeating the date
  already shown directly on the badge. The Release Library grid's compact
  pills are unaffected - they still show that text as their only tooltip.

## [1.0.68] - 2026-09-02

### Fixed

- Manual Watchlist changes now always reconcile the configured Watchlist tag
  with the matching StashApp scene: enabling Watchlist adds the tag even when
  an older sync record exists, while disabling Watchlist removes only that tag
  and preserves every unrelated scene tag.
- Removing a Watchlist tag manually now clears its completed-sync marker so a
  later scheduled Watchlist assignment can add it again normally, while the
  scheduled sync retains its existing cached behavior.

## [1.0.67] - 2026-09-01

### Added

- Settings → Interface now provides persistent per-device controls for how many
  nearby releases and screenshots Release Details preloads. Both accept any
  non-negative whole number, with `0` disabling that preload type.

### Fixed

- Release Details metadata overflow panels now remain open while the pointer
  moves into them, allowing Actress, Studio, Label, Tag, and Site entries in
  the panel to be clicked reliably.

## [1.0.66] - 2026-09-01

### Added

- Docker deployments now apply configurable `PUID` and `PGID` ownership to the
  JAVBeacon application-data mount before starting; the legacy `GUID` spelling
  remains accepted as a group-ID fallback.

### Changed

- Release Details now preloads detail data, covers, screenshot indexes, and the
  first screenshots for eight nearby releases in each direction.
- Job History uses larger, more readable typography and spacing, with Duration
  displayed in its own dedicated column.

### Fixed

- Existing and newly created files under `/app/data` now receive the requested
  Docker user/group ownership and group-writable permissions, preventing host
  permission errors for matching users such as `1000:1000`.
- Opening and closing Release Details no longer incurs smooth-scroll or costly
  desktop background-repaint delays.
- Navigating to an uncached release keeps the current details visible until the
  next release and its imagery are ready, eliminating the black refresh and
  flicker during Previous/Next navigation.

## [1.0.65] - 2026-09-01

### Changed

- Job History now labels scheduled scans with their schedule name and total
  monitoring-site scope, and displays explicit start, end, and duration values.
- Multi-site scheduled scans remain one concise aggregate history entry, with
  their result summary clearly stating how many monitoring sites it covers.

### Fixed

- Scheduled scan history no longer appears to belong only to the final site
  processed, such as Tentacle; durable schedule metadata is now kept separate
  from the live per-site progress fields.
- Scrape history now records the actual execution start instead of the time a
  scan entered the queue.

## [1.0.64] - 2026-09-01

### Changed

- Release Details metadata chips now use compact spacing without dot
  separators, and clickable Sites match the visual treatment of Tags without
  external-link arrows.

### Fixed

- Actress, Studio, Label, Tag, and Site overflow controls now reveal their full
  values on desktop hover and click or touch.
- Metadata fitting now moves an entire chip into overflow instead of displaying
  a partially clipped value.

## [1.0.63] - 2026-09-01

### Changed

- Release Details now opens cached releases immediately, closes without waiting
  for browser-history synchronization, and uses a fast, subtle transition when
  navigating between releases.

### Fixed

- Toggling Watchlist, Notification, or Monitoring no longer rebuilds the active
  Release Details view or temporarily removes its screenshot carousel when the
  confirmation notification appears.
- Stale detail responses from rapid navigation or closing are now ignored, and
  live-update echoes no longer cause a second distracting redraw.

## [1.0.62] - 2026-09-01

### Fixed

- Explicitly closing Release Details now exits directly to its originating
  view instead of stepping backward through previously viewed releases, while
  browser Back and Forward continue to navigate the release history.

## [1.0.61] - 2026-09-01

### Changed

- Release Details Downloading and StashApp status controls now match the
  rectangular geometry, sizing, and primary typography of the action buttons.

### Fixed

- Download submission notifications now remain visible above the Search &
  Download dialog instead of being hidden behind the modal.

## [1.0.60] - 2026-09-01

### Added

- Release Details actress and tag rows now preserve complete values, showing
  overflow on desktop hover or from a compact mobile overflow control.
- Release Details site names now open the corresponding Monitoring Sites entry
  in a new tab, scroll it into view, and highlight it temporarily.
- Browser Back and Forward now restore main application views and previously
  visited releases without losing the Release Library context.
- Saving a newly created monitoring site now offers to queue an immediate Full
  refresh through the site's online end.

### Changed

- The Release Details "Monitoring sites" metadata label is now the more compact
  "Sites" label.
- Studio, Label, and Sites now use the same complete-value controls and
  responsive overflow treatment as Actresses and Tags.
- The Release Details download action now uses the Search icon and compact
  "+ Download" label for consistent alignment without text clipping.
- Site-group schedules now explain that their page limit is a maximum and that
  New releases only stops early after a page with no unseen releases.
- StashApp local-library sync logging now reports job start, completion, and
  errors without emitting repetitive per-batch progress entries.

### Fixed

- Release Details no longer displays an empty Story block when no description
  is available.

## [1.0.59] - 2026-08-31

### Added

- Download Activity can sort active downloads by ETA or completion percentage.

### Changed

- Release Details now places Downloading, Downloaded, and StashApp availability
  in a dedicated compact Status section above Discover and Tracking.
- Status, Discover, and Tracking use integrated section headers with denser,
  consistently aligned controls on desktop and mobile.
- Release metadata rows are shorter with slightly larger text, and recovered
  vertical space is used to show more of the release story.
- Actress and tag values are presented as individually readable controls with
  subtle separators instead of running together.
- Downloading remembers its sort field and direction independently from the
  Completed, Stalled, and Failed activity tabs.

## [1.0.58] - 2026-08-31

### Changed

- Actress and genre/tag metadata now lives exclusively in ordered normalized
  relationship tables. Existing SQLite and PostgreSQL installations backfill
  and verify those relationships before the duplicate release columns are
  removed.
- JAVLibrary and Akiba/GIGA detail parsing now carries structured actress lists
  through refreshes and legacy imports, preserving individual performers and
  names containing commas.
- Release Details presents Downloaded and In StashApp states as compact,
  readable information controls and no longer shows the redundant redownload
  explanation.
- Release Library and notification cards use the same Watchlist, Notification,
  and Monitoring controls as Release Details, with a compact icon-only actions
  menu to preserve card space.
- Added and Updated metadata rows now use consistent sizing in Release Details.

### Fixed

- Partial listing refreshes no longer risk clearing structured actress or tag
  relationships learned from a detail scrape.
- Release Details notifications now use a stable overlay instead of moving the
  global toast inside the dialog, preventing the screenshot area from blinking
  or briefly reflowing.

### Performance

- Added release-and-position indexes for ordered actress/tag reads while
  retaining normalized-name indexes for Release Library search and filters.

## [1.0.57] - 2026-08-31

### Changed

- Stacked the recent and older monitored-search schedule cards vertically and
  moved Schedule to the final Download Monitoring tab position.
- Release Library cards now use the same Watchlist, Notification, and
  Monitoring labels, checkbox states, colors, ordering, and icon language as
  Release Details.
- Release Library action menus now use matching icons and emphasis for Search,
  Search & Download, Update details, and Open detail.
- Narrow Release Library cards arrange state controls in two columns to keep
  their labels legible.

### Fixed

- Bottom-right notifications shown inside Release Details no longer disturb or
  temporarily hide the screenshot area.
- Prevented monitored-search schedule status and control text from becoming
  compressed and mangled in the side-by-side layout.

## [1.0.56] - 2026-08-31

### Added

- Download Activity now has a Stalled only filter covering torrents with zero
  seeders or no recorded last-seen-complete time.
- Download Activity supports substantially larger cover previews for easier
  identification.

### Changed

- Moved the recent and older monitored-search schedules into a dedicated
  Schedule tab under Download Monitoring.
- Renamed the monitoring tabs to Monitored releases and Download search
  history for clearer navigation.
- Download Activity keeps its date sorting controls visible beside Clear
  selection instead of hiding them inside the foldable filter panel.
- Release Details uses compact, aligned Search, Search & Download, and Update
  details actions, plus consistently labelled Watchlist, Notification, and
  Monitoring state buttons with clear checked and unchecked styling.
- Release Details now presents Added and Updated timestamps as separate
  metadata rows.

### Fixed

- Kept Search & Download on one line on mobile and normalized action icon size,
  text alignment, and spacing across the Release Details action rows.
- Kept the mobile Release Details close control inside browser safe areas so it
  no longer clips against the top or right viewport edge.

## [1.0.55] - 2026-08-31

### Added

- Monitoring Sites can now automatically enroll genuinely new future releases
  in scheduled monitoring after a safe first-scrape baseline has been
  established. Releases first associated with the site on later runs qualify
  only when their release date matches or exceeds the site's prior newest date.
- Added per-site Bulk monitoring with cover previews, release IDs and dates,
  multi-selection, and actions to monitor or stop monitoring selected releases.
- Monitored-release results now explain whether monitoring was set manually,
  migrated from a former site rule, or created by future-release monitoring for
  a named site.

### Changed

- Removed Monitoring Sites' automatic Search + Download modes and their runtime
  download behavior. Site discovery can now enroll releases for the existing
  monitored-release scheduler without immediately searching or downloading.
- Existing sites configured for the former Future Releases mode migrate to
  Automatically monitor future releases. Previously enrolled releases remain
  explicitly monitored so upgrades do not discard the existing queue.
- Completed the Watchlist terminology cleanup across source, UI, persisted
  models, documentation, and changelog text, while retaining lossless migration
  support for installations created with the retired naming.

### Fixed

- StashApp local-library sync now clears monitoring when a release transitions
  from unavailable locally to locally available, preventing accidental
  redownloads.
- Newly added Monitoring Sites establish their release-date baseline without
  enrolling existing page-one results, and wait until a later scrape before
  automatic future-release monitoring can activate.

## [1.0.54] - 2026-08-30

### Changed

- Completed the Watchlist rename throughout the application internals,
  including domain models, API fields and endpoints, database columns and
  tables, StashApp synchronization, schedules, filter state, keyboard
  shortcuts, tests, styles, and documentation.
- Existing SQLite and PostgreSQL installations automatically migrate all
  Watchlist marks, timestamps, Stash sync history, settings, preferences, and
  saved filters to the canonical names during startup, then remove the retired
  database objects without losing state.

## [1.0.53] - 2026-08-30

### Added

- Replaced the old text and emoji state markers with consistent outline icons:
  a movie Watchlist, modern notification bell, and release-watcher eye.

### Changed

- Renamed the former watch-state terminology to Watchlist throughout the interface,
  settings, filters, messages, documentation, schedules, and application logs.
- On touch devices, tapping the Downloading pill now opens and closes its live
  details without opening the torrent source; desktop clicks still open the
  source while hover and keyboard focus expose the same details.
- Renamed the `Updated` sorting option to `Date updated` and placed it directly
  below `Date added` in the Release Library, Download Activity, and monitored
  release sorting menus.
- Reduced fullscreen screenshot navigation and close controls on phones and
  tablets to responsive 48–64 pixel targets in portrait and landscape.

### Fixed

- Release Details now allows the complete Downloading telemetry card to escape
  its compact action layout, so ETA, seeds/peers, last seen complete, and date
  added are all visible on desktop as well as mobile.

## [1.0.52] - 2026-08-30

### Added

- Release Details now exposes live torrent telemetry from its compact
  Downloading pill: ETA, seeds/peers, last-seen-complete time, and date added
  appear in a clear hover/focus panel without crowding the main controls.
- On iPhone and iPad, tapping the center of the Release Details cover starts
  its screenshot slideshow; tapping the center again stops it and restores the
  cover. Existing swipes and outer-edge navigation zones remain unchanged.
- Release Details updates the browser tab title to the active release ID and
  title, including during rapid next/previous navigation.

### Changed

- Release Library and Release Details slideshow intervals and the Release
  Details hover delay are now remembered independently per browser/device,
  alongside the existing per-device interface and cover sizing preferences.
- Download telemetry is fetched only for the open release, keeping large
  Release Library searches and card queries free of additional per-row work.

### Fixed

- Prevented Firefox from briefly flashing the next release ID at the top-left
  of the cover while its image loads during Release Details navigation.
- Guarded asynchronous Release Details rendering and browser-title updates so
  a slower previous request cannot overwrite the currently active release.

## [1.0.51] - 2026-08-30

### Added

- Site group schedule cards now show their next three scheduled runs directly
  beside their controls, including a combined forecast when the group contains
  multiple monitoring sites.
- Release Details now shows consistent, subtle position indicators for both
  release navigation and the cover-area screenshot slideshow, while the
  fullscreen screenshot viewer uses the same "N of M" presentation.

### Fixed

- Site group schedules now load monitoring sites before their settings are
  rendered, preserve selected sites when saved, and restore selections when an
  existing schedule is edited.
- Restored the missing Release Details navigation position indicator and
  stopped brief release-ID changes in the browser title during navigation.
- Historical backfill restores its persisted last-stop timestamp after an app
  restart and no longer renders an unset timestamp as `1-1-1`.

## [1.0.50] - 2026-08-30

### Added

- Release Details now shows a hover-triggered screenshot slideshow on the
  cover, matching the Release Library grid's cards but with its own
  configurable hover delay (default 0.5s) and playback speed, looping
  endlessly while hovered.
- Release Details navigation shows a subtle "N of M" position indicator
  above the cover, so it stays clear during Next/Previous navigation how
  many releases remain in the current list.
- Added Site group schedules: any number of independently named scrape
  schedules, each covering a chosen subset of monitoring sites with its own
  Quick/Full/New mode per site, alongside the existing Quick refresh, Full
  refresh, and New Release Only schedules.
- Add Monitoring Site can now auto-detect the Category and ID from a pasted
  JavLibrary URL and auto-generates the Source URL from Category and ID,
  while still allowing the URL to be edited by hand.

## [1.0.49] - 2026-08-30

### Added

- Added a temporary Release Library search endpoint at `/search?q=...` for
  browser keyword and custom-search integrations. It opens the All tab, fills
  the general wildcard field, applies the query for that visit, and leaves the
  user's saved filters unchanged.
- Added automatic OpenSearch discovery on both the main interface and login
  page, allowing Firefox to detect JAVBeacon as an installable search engine.

### Changed

- Release-card Actress, Studio, and Label metadata now uses people, building,
  and imprint icons instead of ACT/STU/LBL badges, with stable grid columns so
  long actress names cannot overlap the other metadata.
- On touch devices, the fullscreen screenshot close button now matches the
  size of its previous/next controls, and the Release Details close button
  matches its cover navigation controls.
- Unauthenticated browser searches now return to their original query after
  sign-in instead of opening the default Release Library state.

### Fixed

- Release Details and fullscreen screenshot swipes no longer scroll the
  Release Library behind the active overlay; the library's exact position is
  restored when Release Details closes.
- Horizontal touch gestures are captured before completion so diagonal or
  fast swipe momentum cannot escape into the background page.
- Restored the Release Library filter toolbar on desktop while retaining the
  remembered fold state on iPhone and iPad only.
- Temporary URL searches can no longer be written into saved preferences by a
  delayed initialization save.
- Aligned the Release Library result counter to the page-title text baseline.

## [1.0.48] - 2026-08-30

### Added

- Release Library filters and tabs now live in one compact foldable panel on
  iPhone and iPad, shared across All, Released, Upcoming, Local, and Watchlist so
  the cover grid can use more of the screen.
- The mobile Release Library filter panel remembers its folded state through
  page reloads, sessions, and application restarts.
- Restored subtle translucent previous/next controls over the Release Details
  cover on iPhone and iPad.
- Horizontal swipes across the Release Details content now navigate releases,
  while fullscreen screenshot swipes work across the complete viewer; moving
  content left advances and moving it right returns to the previous item.

### Changed

- Fullscreen screenshot navigation buttons are now twice their previous touch
  size on both iPhone and iPad.

### Fixed

- The fullscreen screenshot close button is now anchored to the safe top-right
  viewport corner on iPhone in both portrait and landscape, matching iPad.
- Rapid taps on mobile Release Details and screenshot controls no longer invoke
  the browser's double-tap page zoom, while ordinary pinch zoom remains
  available.
- Horizontal release and screenshot swipes no longer select text, drag images,
  or open the iOS touch callout.

## [1.0.47] - 2026-08-30

### Added

- Release Details cover navigation now uses the outer 20% on either side as
  forgiving previous/next tap and click targets, while keeping visible controls
  above those invisible navigation areas.
- Fullscreen screenshots use the same outer-20% navigation targets without
  removing their existing arrow buttons.

### Changed

- Fullscreen screenshot arrows now provide larger responsive touch targets on
  iPad and a smaller, proportional size increase on iPhone.
- Mobile Release Details no longer overlays previous/next buttons on the cover
  or changes releases on a swipe; navigation remains available through the
  unobtrusive outer-20% edge tap targets.
- Release Details and fullscreen screenshot close controls remain explicit
  interactive exclusions from the new edge targets, so the top-right close
  action always wins over navigation.

## [1.0.46] - 2026-08-29

### Changed

- Clearing Release Library filters now preserves the persisted minimum and
  maximum days-since-release range instead of silently resetting it.

### Fixed

- Release Details now shows the same compact translucent floating close button
  on iPhone and coarse-pointer iPads, including tablet widths where the former
  phone-only close handle was unavailable. The control uses only a small
  top-right title inset and leaves mouse/desktop layouts unchanged.

## [1.0.45] - 2026-08-29

### Changed

- Release Library, Notifications, Monitored Releases, and Download Activity
  cover sizes plus the global interface scale are now remembered separately by
  each browser/device. Existing shared values seed a device on first use, and
  legacy saved filters no longer overwrite its display scales.

## [1.0.44] - 2026-08-29

### Fixed

- Release Details now remains exactly within the physical phone viewport at
  enlarged interface zoom levels, keeping cover and screenshot navigation
  controls inside the right edge and preventing portrait information clipping.
- Phone landscape no longer applies the desktop touch-scroll lock, so the
  information pane can be scrolled independently while the cover and
  screenshot strip remain in place.

## [1.0.43] - 2026-08-29

### Added

- Mobile Release Details now includes scalable previous and next buttons over
  the cover for deliberate one-handed navigation alongside swipe gestures.

### Fixed

- Phone landscape Release Details now keeps the screenshot strip visible and
  gives the right-side information pane its own touch scrolling.
- Mobile layouts hide the overlapping desktop fullscreen/navigation chrome and
  keep the top-edge close action available in both orientations.

## [1.0.42] - 2026-08-29

### Added

- Mobile Release Details now supports natural swipe navigation on the cover
  and fullscreen screenshots. Cover edge taps move to the previous or next
  release, and a compact top-edge handle closes the detail view.

### Fixed

- Mobile Release Details now keeps screenshots directly below the cover and
  uses an opaque, isolated background so artwork from the library behind the
  dialog cannot bleed through.
- The mobile bottom navigation now fits all eight destinations in one stable
  row instead of wrapping Settings onto a second line.

## [1.0.41] - 2026-08-29

### Fixed

- The historical backfill's manual controls moved into Settings ->
  Maintenance in 1.0.40, but that also removed its only progress
  indicator from the Jobs/Activity page, leaving no way to tell it was
  running without switching to Settings. Added a read-only status card
  back on the Jobs/Activity page, next to "Job progress", showing the
  current run's state, page, and per-run counts - fed by the same
  status poll already driving the Settings panel, so it adds no extra
  request traffic. A "Manage in Settings -> Maintenance" button on the
  card jumps to the full resume/priority/start/stop controls instead of
  duplicating them.

## [1.0.40] - 2026-08-29

### Fixed

- The manual JavLibrary historical backfill's genre/star/maker index crawl
  could spiral into ever-growing, broken URLs
  (`.../genres.php/genres.php/genres.php/...`), because the shared link
  resolver always appended a trailing "/" to the current page's URL before
  resolving a relative link against it - turning a `.../genres.php` "file"
  URL into a synthetic "directory" and causing a self-referential or
  pagination-style link on that page to resolve underneath itself instead
  of beside it. The resolver now follows standard URL relative-reference
  rules directly, so historical backfill discovery stays on the actual
  genre/performer/maker index pages instead of drifting into
  never-ending, malformed ones. The unrelated Akiba/GIGA scraper, which
  uses the same resolver, is unaffected.

### Added

- The historical backfill now fetches multiple release detail pages at
  once when more than one Byparr/FlareSolverr instance is configured,
  instead of always fetching one at a time - a new "Max instances -
  Historical backfill" setting (Settings -> Scraping -> Byparr /
  FlareSolverr) caps how many it may use concurrently, matching the
  existing per-schedule-type caps; leave it blank to use every enabled
  instance. Manual scrapes and scheduled scans continue to get first pick
  of a free instance over backfill work.

### Changed

- Moved the historical backfill's manual controls, and the cover and
  screenshot cache maintenance panels, out of the Jobs/Activity page and
  the Storage settings tab into a new Settings -> Maintenance tab, since
  all three are backfill/maintenance work rather than everyday scrape
  jobs. Also removed a long-unwired "Screenshot backfill" status card
  that had been stuck on the Jobs page showing "Loading screenshot
  status..." with no live updates.

## [1.0.39] - 2026-08-29

### Fixed

- A download's completion (or removal) event pipeline could occasionally
  be started twice for the same event, running its steps back-to-back,
  because the decision to start it was based on the pipeline's stored
  "running" state - which only actually gets written once the shared
  pipeline worker picks the job off the queue, not the moment it's
  queued. A qBittorrent status poll landing in that small gap (more
  likely with the faster, configurable poll interval added in 1.0.38)
  would see the same "not started yet" state and queue a second run of
  the exact same event for the exact same download. Pipeline runs are
  still never executed concurrently and always run in the order they
  were triggered - only the double-queuing itself is fixed - so any
  shell command, StashApp call, or other configured step tied to a
  completion or removal event now runs exactly once per event, not
  occasionally twice.

## [1.0.38] - 2026-08-29

### Fixed

- Download Activity could get stuck showing stale pre-completion progress
  for a torrent indefinitely, even well after qBittorrent itself showed it
  fully seeded and moved on. qBittorrent status polling ran on a single
  goroutine and used to block entirely on that download's "download
  completed" event pipeline (a shell step, a StashApp call, a large file
  move) before saving anything - a slow or stuck pipeline step for any one
  download froze status updates for every download, not just that one,
  until the pipeline finished. Polling now saves qBittorrent-derived
  progress/status immediately and runs the completion/removal event
  pipeline in the background instead of blocking on it - pipelines still
  never run concurrently with each other and still run in the order they
  were triggered.
- A bug triggered while polling one specific download (a bad file list, an
  unusual pipeline config, ...) could previously take down the entire
  application, since an unrecovered panic in any goroutine - including the
  dedicated qBittorrent polling one - terminates the whole process, not
  just that goroutine. Polling is now isolated per download and per poll
  cycle, so a problem with one download is logged and the rest of Download
  Activity keeps updating on schedule instead of the app going down.

### Added

- qBittorrent status polling interval is now configurable (Settings ->
  Downloads -> Poll interval (seconds), minimum 2s, default 5s) instead of
  a fixed 1 minute, so Download Activity tracks qBittorrent close to real
  time. This is safe to run much faster than before: the per-tick
  qBittorrent request is already a single batched call regardless of how
  many downloads are active, and the one qBittorrent call that does scale
  per download (fetching a torrent's file list) is now only made the first
  time a download sees a torrent and once more on completion, not on every
  single poll.

## [1.0.37] - 2026-08-29

### Fixed

- Quick refresh now backfills Label, Studio, Director, Actress, release
  date, Genres, and Duration/Story on an existing release when the freshly
  scraped detail page has a value and the stored release does not - most
  commonly Label on a release added before JavLibrary Label parsing
  existed. Quick still never overwrites a field the release already has a
  value for (that remains Full refresh's job), and this backfill - like
  Quick's existing cover/screenshot repair - preserves updated_at so it
  does not affect "sort by date updated." Previously Quick left every one
  of these fields untouched no matter how long they had been blank.
- Quick/Full/New-releases scan jobs no longer silently fall back to
  listing-page-only data (title/cover, no Label, Studio, Genres, release
  date, or screenshots) for a release whose detail-page fetch fails during
  the scan's concurrent per-item fetch, while still reporting the release as
  a normal "added"/"updated" success. That failure is now surfaced on the
  job's Error field (visible on the Jobs page) with a count and a short
  sample of affected video IDs, so a struggling or overloaded solver
  (Byparr/FlareSolverr) shows up immediately instead of releases quietly
  never getting their Label or screenshots filled in. Manual "Update
  details" and the screenshot-backfill job were unaffected by this bug and
  are unchanged.

### Added

- Quick/Full/New-releases scans now give a release detail-page fetch one
  extra try, after a longer separate cooldown, if it still failed after
  its own existing fetch-and-retry cycle (a Cloudflare block, a solver
  error, or a transport failure) - most useful when a Byparr/FlareSolverr
  instance is only transiently overloaded by a scan's concurrent batch of
  detail fetches rather than genuinely down. This does not apply to a
  structurally invalid detail page (a real page-shape change), which
  remains a terminal, non-retried failure as before, and it only ever adds
  one bounded extra attempt - it does not repeat the existing retry cycle
  itself, to avoid compounding delay under sustained solver trouble.

### Changed

- Removed the JAVBEACON_PAGE_LIMIT environment variable. It only ever
  seeded the "page_limit"/"full_refresh_page_limit"/
  "new_release_refresh_page_limit" settings on first startup - once an
  install exists, those settings (editable in Settings -> Scraping) are
  what every scan actually reads, so the environment variable had no
  effect beyond that one-time seed and was a confusing, effectively dead
  knob. The seeded default (5) is unchanged; change the settings
  themselves to adjust it going forward.

## [1.0.36] - 2026-08-29

### Fixed

- PostgreSQL startup now applies the 30-second timeout only to establishing and
  validating the database connection. Schema migrations, data migrations, and
  release-preference backfills are no longer cancelled when large databases
  require more than 30 seconds to upgrade.
- PostgreSQL migration progress reporting is preserved throughout startup and
  database recovery while long-running migration operations use the normal
  migration context.

### Changed

- Database startup now separates the short PostgreSQL connection timeout from
  potentially long-running schema and release-preference migration work.

## [1.0.35] - 2026-08-29

### Added

- Added a manual JavLibrary historical catalog backfill at default priority
  500. It discovers genre, performer, and maker indexes, persists source and
  release checkpoints across restarts, skips releases already present from any
  normal monitoring source, relocates date-sorted resume boundaries to catch
  releases inserted while offline, and shows separate historical and current-
  run progress on the Jobs page.
- Database migrations now have a temporary startup web interface showing the
  active connection/schema/data phase, current table or index, phase progress,
  and retry attempt. The PostgreSQL recovery page also shows migration progress
  during automatic and manual retries.

## [1.0.34] - 2026-08-29

### Fixed

- PostgreSQL upgrades no longer rewrite every release while initializing the
  materialized ignore-tag/title state. Only releases whose preference state
  actually changes are updated, preventing large libraries from exceeding the
  startup connection deadline and incorrectly entering database-recovery mode.

## [1.0.33] - 2026-08-29

### Changed

- Rebuilt and republished the current JAVBeacon application and multi-platform
  container package as the next patch release.

## [1.0.32] - 2026-08-29

### Added

- PostgreSQL large-library migrations now enable `pg_trgm` and add targeted
  trigram, sort, notification, and download-status indexes for release search,
  metadata filters, common tabs, and card status lookups.
- Release Library batches now use stable cursor pagination for all normal
  sorts, avoiding increasingly expensive database offsets as users browse
  deeper into large libraries.

### Changed

- Release Library cards use a dedicated lightweight response that omits
  detail-only fields while retaining the cover, screenshot, tracking, and
  download state needed by the grid. Full metadata is still loaded when a
  release is opened.
- Matching release counts now load independently from the first card batch and
  are cached briefly, so an exact count no longer blocks visible results.
- Metadata suggestions are debounced, cancel stale requests, query normalized
  metadata directly, and use a bounded five-minute application cache.
- Ignore-tag and ignore-title results are materialized on releases and updated
  when rules or release metadata change, replacing repeated per-row ignore
  evaluation on every library query.

### Fixed

- Rapid search, filter, and sort changes now cancel superseded card and count
  requests instead of allowing stale work to delay the current result set.
- Quick and Full refresh now cache refreshed covers and screenshots for
  existing JavLibrary releases even when their saved metadata does not need
  updating. Artwork-only changes no longer alter the release's metadata
  `Updated` timestamp.
- Live scrape and sync updates now refresh the active Release Library query
  instead of inserting a release directly into the visible grid, so current
  tabs, saved filters, Hide Local, and ignore rules remain respected.

## [1.0.31] - 2026-08-28

### Changed

- Scrape page-count fields no longer cap values at 500. "All pages" is now
  genuinely unbounded, while both explicit high limits and all-pages scans
  stop at the provider's detected final listing page (with empty and repeated
  page safeguards still preventing runaway pagination).

## [1.0.30] - 2026-08-28

### Added

- New configurable keyboard shortcut (default: `p`) opens the fullscreen
  screenshot view straight from Release Details, alongside the existing
  Toggle fullscreen shortcut.

### Changed

- Moved the Release Library's release count ("N of total") up next to the
  "Release library" heading as a small badge, instead of its own row above
  the filter tabs - frees up vertical space and reads more like a page
  subtitle.

### Fixed

- The sidebar's Sign out button and version badge could render below the
  visible window at a UI Zoom setting above 100%, since the sidebar's height
  was computed from the raw viewport before zoom instead of the same
  zoom-corrected value already used elsewhere for full-screen dialogs.
- The version badge disappeared entirely when the left navigation was
  collapsed instead of staying visible in the narrow column.
- "Min days since release" and "Max days since release" could be lost after
  an app restart if the browser tab closed or reloaded within the short
  debounce window after changing them; they now save immediately, matching
  the other Release Library toggle filters.

## [1.0.29] - 2026-08-28

### Added

- Closing Release Details after browsing now returns the Release Library to
  the release that was actually being viewed, focuses its card, and briefly
  highlights it. This also waits for fullscreen mode to finish closing before
  restoring the library position.

### Changed

- Release Library infinite scrolling now preloads the next batch at roughly
  60% page depth and appends cards without rebuilding the existing grid, for a
  smoother continuous scroll. Release Details starts preloading when 25 items
  remain in its navigation list.
- Release Details screenshot thumbnails and their rail are 30% taller, with
  proportionally wider thumbnails for easier previewing.

### Fixed

- Loading a saved filter set now synchronizes its stored tab with the visible
  Release Library tab. This fixes Hide Local appearing ineffective and Fade
  Local dimming every card when a preset's hidden state was still on Local.
- A filter or preset change made while an older release batch was still
  loading can no longer be blocked by that stale request or append stale rows.
- Release Details navigation now retains its live Release Library context
  after the first Next/Previous action, allowing later batches to keep loading.
- Bottom-right notifications are now a contained fixed overlay and no longer
  resize or shift the cover and screenshot layout in Release Details.
- The sidebar gives the version badge guaranteed space so the full version is
  readable instead of being clipped by the Sign out control.
- Download Activity's dynamically-added filter, sort, cover-size, and page-size
  controls now share a consistent bottom baseline and control height.
- Release workflow tests now validate against the current application version,
  fixing the stale v1.0.27 assertion that stopped the v1.0.28 GHCR build.

## [1.0.28] - 2026-08-28

### Changed

- StashApp Integration Sync now shows live phase, checked/total, percentage,
  current release, matched/updated counts, and a progress bar. Server logs now
  record the start, periodic progress, completion totals, duration, and errors.
- Release cards now identify actress, studio, and label metadata with compact
  color-coded `ACT`, `STU`, and `LBL` role markers while keeping the metadata
  on one space-efficient line.

### Fixed

- JavLibrary listing responses classified as `INVALID` because they contain
  no `.video` or `.id` entries are now treated as transient and receive the
  normal two retries with solver cooldown/backoff before the scrape fails.
  Invalid detail-page structures remain terminal rather than being retried.
- Local StashApp matches now fetch the scene's always-available `created_at`
  through a dedicated required query instead of coupling it to optional
  playback statistics. This guarantees "Added Locally" uses the same data as
  StashApp's `sortby=created_at` ordering. Regular scheduled local-library
  integration syncs explicitly use this same complete sync path.
- "Added Locally" now switches to newest-first when selected, matching
  StashApp's `created_at` descending sort. Releases whose StashApp creation
  timestamp has not synchronized yet are kept at the end instead of appearing
  above releases with a known date on PostgreSQL.
- Labels in Release Details are now clickable and open a new Release Library
  page filtered to that label, matching actress, studio, and tag links.
- Job History no longer presents every accepted or rejected torrent search
  candidate as a separate download job. Those detailed audit records remain
  stored, while Job History shows only meaningful download lifecycle entries.

## [1.0.27] - 2026-08-28

### Added

- Manual "Update details" refreshes now run concurrently across every idle
  Byparr/FlareSolverr instance instead of queuing strictly one at a time.
  Starting several release updates at once (or one alongside a running
  scheduled scan) now dispatches each to its own goroutine immediately;
  contention for Byparr instances is resolved by priority, so a manual
  update still jumps ahead of a lower-priority scan's own per-item fetches.
  The Jobs page now lists every release update currently running under
  "Updating now", and "Stop job" cancels all of them along with any active
  scan.

### Fixed

- SQLite installs could hit a hard "database is locked" error when two
  scrapes tried to write at the same time (most reachable via the new
  concurrent release-update dispatch above) - SQLite access is now routed
  through a single pooled connection so writers queue behind SQLite's own
  locking instead of racing across separate connections.

- JavLibrary's "Label" field (e.g. "Otona No Drama") was never scraped into
  release details — the value was parsed but then dropped while merging the
  detail page into the release, so it always showed as blank. Release cards
  and the notifications list also mislabeled the monitoring site's own name
  as if it were the release's Label (which happened to duplicate the Studio
  name in some cases, e.g. showing "Attackers" twice); they now show the
  actual Label field.

## [1.0.26] - 2026-08-28

### Added

- Release Library's infinite-scroll and release-details paging batch size
  is now configurable in Settings → General (Release Library → Batch
  loading), defaulting to 100 releases per batch.

### Changed

- Renamed the Local tab's "Added to StashApp (local)" sort option to
  "Added Locally" so it no longer gets clipped in the sort dropdown.

### Fixed

- The Released tab's date-window "Min/Max days since release" fields now
  accept negative values, letting the window extend past its start date
  instead of only backward from it. Fixing the underlying date math also
  fixed the Upcoming tab's "days in future" window, which was silently
  a no-op (always showing just today) since it was added - it now
  actually limits results to that many days ahead.
- The Local tab's "Added Locally" sort now actually sorts by when the
  scene was created in your StashApp library (its own `created_at`),
  instead of by when JAVBeacon happened to notice the match during a
  sync. Existing matches are backfilled with JAVBeacon's first-seen date
  as a placeholder until their next StashApp sync fills in the real value.
- Scheduled scrapes now actually run at the time you configure. The
  container previously had no timezone database installed, so "server
  local time" silently meant UTC no matter what the host machine's clock
  said - a schedule set for 02:00 could fire at 04:00 (or any other offset)
  for anyone outside UTC. The image now ships tzdata and Compose passes
  through a `TZ` environment variable (defaulting to UTC, unchanged from
  before) - set `TZ=<your IANA zone>` (e.g. `Europe/Amsterdam`) in `.env`
  and restart to have every schedule follow your actual local clock.

## [1.0.25] - 2026-08-28

### Added

- Release Library now remembers a separate sort order per tab. All defaults
  to Date Added (newest first), Released and Upcoming default to Release
  date (newest first), Local defaults to a new "Added to StashApp (local)"
  sort (newest first), and Watchlist defaults to a new "Added to Watchlist"
  sort (newest first) tracking when each release was most recently added.
  Changing the sort on one tab no longer affects the others.
- The Upcoming tab gained a foldable "Upcoming window" panel, matching the
  Released tab's date-range panel, letting you cap upcoming releases to a
  configurable number of days from today. The chosen window (and whether the
  panel is expanded) is remembered across visits.
- Release Library now loads releases in batches as you scroll or navigate
  through release details, instead of stopping at the first 500 results.

### Changed

- Consolidated the duplicate Logs and Missing Library Files headings into
  compact status/action rows, leaving more vertical room for their content.
- Release Library's heading and release count now share a single compact
  row instead of repeating "Release Library" twice.

### Fixed

- Missing Library Files now persists its last completed scan timestamp and
  result counts across page reloads and application restarts. Existing
  installations recover the most recent scan time from stored missing-file
  rows, and a missing status can no longer render as a year-1 date.
- Settings could silently fail to save with no error or notification when an
  invalid value was left in a field on a tab you'd since switched away from
  (the browser's built-in validation couldn't show its message on a hidden
  field, so the whole save silently aborted). Settings now always saves and
  reports any validation problems through its own error message.

## [1.0.24] - 2026-08-28

### Changed

- Published a maintenance release containing the current scheduling,
  release-filter, screenshot-backfill, and multi-Byparr improvements.

## [1.0.23] - 2026-08-28

### Added

- Settings → Scraping now supports configuring multiple Byparr/FlareSolverr
  instances instead of just one, each with its own priority - add a row per
  reachable instance to spread JavLibrary scraping across them concurrently.
  A request always picks the highest-priority free instance first and falls
  through to the next free one once it's busy, so every enabled instance
  ends up used under load without needing to pick one by hand. Manual
  "Update details" always gets first pick of a free instance over a
  background job. Quick refresh, Full refresh, New releases only, and the
  Screenshot backfill maintenance job can each be capped to a maximum number
  of instances independently (Settings → Scraping · blank means "use every
  enabled instance").

### Changed

- Screenshot backfill no longer bumps a release's "date updated" - it was
  possible for a backfill run that merely confirmed or repaired an old
  release's screenshots to jump that release back to the top of "sort by
  date updated" in the Release Library, with no other change to explain why.
  Backfill also now processes multiple releases concurrently (using the
  configured Byparr instance pool, see above) instead of one at a time.

## [1.0.22] - 2026-08-27

### Added

- Scrape schedules now have explicit Basic, Advanced, and Power user modes.
  Basic combines an interval with an optional first-run time; Advanced runs
  on selected weekdays at a chosen time while enforcing the interval as a
  minimum gap; Power user uses a five-field cron expression as the complete
  schedule. Only fields used by the selected mode are shown.
- The Released tab's date window now has a selectable start date, defaulting
  to today, and lives in a compact collapsible panel. The start date, day
  offsets, and folded state persist across reloads and application restarts;
  saved filter sets also capture the effective start date and both offsets
  for exact site- or studio-specific release windows.

### Fixed

- Settings now save atomically in one request. Validation failures from the
  main form or schedule fields are shown beside the Save button and can no
  longer be swallowed by a later partial save that incorrectly reports
  success.
- Day and week suffixes such as `7d` and `2w` now pass the same validation
  for Download Monitoring and StashApp schedules that their schedulers use.
- Restored the next scheduled run date to both recent and older Monitored
  releases job summaries by reconnecting them to the live schedule forecast.
- Monitored releases jobs with no completed run now show `never` instead of
  formatting Go's zero-value timestamp as a misleading year-1 date.

### Changed

- Scrape schedule configuration no longer relies on hidden field precedence;
  each job saves one explicit schedule mode and its live forecast reflects
  that mode.

## [1.0.21] - 2026-08-27

### Added

- The Jobs page's "Job progress" section now shows the Screenshot backfill
  maintenance job's own progress (currently-processing release, checked/
  total counts, and how many are remaining) with a progress bar, alongside
  the existing Settings → Storage display - so its progress is visible from
  Jobs without switching views while it runs. Both displays also now show
  the run's total elapsed runtime and an estimated time remaining, computed
  from the job's own actual throughput so far and updated every tick, plus
  the total runtime of the last completed run.

## [1.0.20] - 2026-08-27

### Added

- StashApp sync's Local library sync and Watchlist-tag sync now show their own
  enabled/interval state and next 3 predicted run times directly under
  Settings → StashApp's own controls, the same way Settings → Scraping does
  for the scrape schedules.
- The Release Library's Released tab now has a configurable "Min days since
  release" / "Max days since release" window, remembered across visits. The
  tab always shows the effective date range applied ("Showing releases
  released between X and Y", or "...released up to X" with no minimum set),
  computed from those two settings.
- The card "Actions" menu (Search, Search & Download, Update details, Open
  detail) moved from its own full-width row into the Notify/Watchlist/Monitor
  button row, as a compact "⋯ Actions" trigger, to save vertical space on
  every card.
- Release Library cards now show as many tag chips as fit on one line
  instead of always exactly 2, adapting to the card's actual width (cover
  size, zoom, and window size); anything that doesn't fit collapses behind
  a "+N" badge whose hover popover lists every tag, unchanged.

### Fixed

- Fixed the Quick refresh / Full refresh / New Release Only scrape
  schedules always displaying "Every 0s · Next run not yet known" regardless
  of their actual configured or default interval - the schedule status
  display simply wasn't looking at the same fallback interval the real
  running scheduler uses, so it showed nothing useful even when the
  schedule was running normally.
- Fixed a schedule interval typed with a "d" (day) or "w" (week) suffix -
  e.g. "7d" or "2w" - being silently rejected everywhere a schedule
  interval is parsed (Quick/Full/New Release Only scrape schedules,
  Monitored releases' recent/older search schedules, and StashApp's sync
  schedules): the typed value looked accepted and was saved and echoed
  back, but the schedule actually kept running on its old built-in default
  interval instead, with no error shown anywhere. These fields now also
  accept "d" and "w" units in addition to Go's usual s/m/h.
- Fixed "Monitored releases (older)" on Download Monitoring getting stuck on
  "Loading schedule…" the first time you navigated to that view in a
  session (as opposed to landing there via a full page reload), instead of
  showing its actual schedule status.
- Removed the generic "Scheduled runs" panel from Download Monitoring - it
  mixed together scrape, StashApp sync, and download-monitoring schedules
  that don't otherwise belong on that page. Each group's status now lives
  next to the settings that actually control it instead (see Added, above).
- The Released tab could show releases whose release date is still in the
  future, because it only checked the release's stored "released" flag,
  which doesn't always agree with the release date. It now also excludes
  anything dated after today by default, and further narrows that with the
  new configurable min/max-days-since-release window described above.
- Fixed the tag chip overflow popover (hover the "+N" badge to see every
  tag) sometimes closing itself before you could reach and click a tag
  inside it.
- The card "Actions" menu (see Added, above) previously opened on mouse
  hover as well as on click, which meant a mouse just passing over a card
  could pop its menu open unintentionally; it now only opens and closes on
  click.

## [1.0.19] - 2026-08-27

### Changed

- Fresh installs now use a 2.5-second Release Library cover screenshot
  slideshow interval by default. Existing saved interval preferences remain
  unchanged.

### Fixed

- Release Library cover screenshot slideshows now track the pointer session
  directly, avoiding a fragile asynchronous hover-state check that could
  prevent the slideshow from starting in Firefox.

## [1.0.18] - 2026-08-27

### Fixed

- Downloading and Downloaded pills now open the exact external torrent
  detail page stored for that download. A missing source URL no longer turns
  into a misleading link back to the current JAVBeacon page.

## [1.0.17] - 2026-08-27

### Added

- Release Details' screenshot rail and the fullscreen viewer's thumbnail
  strip now scroll horizontally with an ordinary mouse scroll wheel, in
  addition to middle-button drag - including trackpoint/trackpad
  press-and-hold scroll gestures that are delivered as wheel events.
- Download Activity's "Open torrent page" link is now a pill showing the
  source site's favicon, matching the style of the other badges next to it.
- The Downloading/Downloaded status pill - in Release Details and on
  Release Library/Notifications cards - now links directly to the torrent's
  detail page when one is known, with the source site's favicon, for both
  the in-progress and completed states.
- Added a collapsible "Filters & sort" panel to Download Activity, collapsed
  by default, to save vertical space.
- Settings → Scraping now shows each of Quick refresh, Full refresh, and New
  Release Only's own enabled/interval state and next 3 predicted run times
  directly under that schedule's controls - the same information the
  "Scheduled runs" panel under Download monitoring already showed, now also
  visible right next to the settings that produce it, and refreshing
  immediately after you save a schedule change.

### Fixed

- Fixed the Release Details panel itself rendering oversized and overflowing
  the browser window at interface zoom levels above 100%, which could push
  its rightmost Discover/Tracking button (e.g. Update details, Monitor
  searches) off the edge of the screen with no way to reach it. Affects only
  the interface zoom setting under Settings → Interface, not your browser's
  own zoom, which was unaffected.
- Download Activity's "Delete complete" status no longer reappears on every
  visit reflecting whatever bulk delete/replace job last ran, possibly long
  ago (e.g. a stale "Delete complete · 0 removed"); it's now shown only for
  a job actually run in the current visit, plus any job still genuinely in
  progress.

## [1.0.16] - 2026-08-26

### Fixed

- Fixed the fullscreen screenshot viewer rendering oversized and overflowing
  the browser window at interface zoom levels above 100%, which could push
  its Next button entirely outside the clickable area. The viewer's own
  frame, and portrait screenshots within it, now stay correctly contained
  at any zoom level.
- The fullscreen screenshot viewer now stops at the last screenshot instead
  of wrapping back to the first when clicking Next, and stops at the first
  instead of wrapping to the last when clicking Prev; the Prev/Next buttons
  disable at those ends.

### Changed

- Redesigned the fullscreen screenshot viewer's Prev/Next navigation as
  circular buttons anchored to the image area's edges, replacing an
  undersized, awkwardly placed control, and kept their position consistent
  regardless of the displayed screenshot's size or aspect ratio.

## [1.0.15] - 2026-08-26

### Changed

- The cover hover slideshow's "Cover screenshot interval (seconds)" setting
  now accepts fractional seconds down to 0.1 (100ms), instead of only whole
  seconds, for a faster slideshow.

## [1.0.14] - 2026-08-26

### Added

- Site monitor scrape jobs can now be preempted by a higher-priority job
  queued while they're running: a long multi-site or multi-page scan pauses
  itself as soon as it finishes whichever release's detail page it's
  currently updating, lets every queued job that outranks it (e.g. a single
  release's "Update details" or a "new releases only" scan) run to
  completion first, then resumes exactly where it left off. Jobs' current
  page/item, added/updated/skipped counts and site progress are unaffected -
  nothing is lost or redone. The Jobs panel and header widget now show a
  "Paused" state naming the job it's waiting on.

## [1.0.13] - 2026-08-26

### Added

- Added a compact, collapsed-by-default "Scheduled runs" panel to Download
  monitoring showing each configurable background schedule's enabled state
  and next 3 predicted run times, covering Monitored releases search
  (recent and older), Quick/Full/New scheduled scrapes, and StashApp local
  library and Watchlist-tag sync.
- Job progress now shows a "Site X of Y monitoring sites" progress bar while
  a scheduled or manual all-sites scrape works through the enabled site
  list, instead of only ever showing progress within whichever single site
  happens to be scraping at the moment.

### Fixed

- Download Activity's qBittorrent reconciliation now matches a download to
  its torrent by hash alone once one is known, instead of falling back to a
  loose name-text match that could silently re-point a download at a
  different, unrelated torrent and keep showing its stale progress.
- A download whose torrent has vanished from qBittorrent for a reason
  JAVBeacon doesn't know about now drops out of Download Activity entirely
  and is recorded as removed (unknown reason), instead of staying stuck
  forever under its last known status and blocking that release from being
  searched/downloaded again.
- Monitored releases search, StashApp sync, and notification/RSS schedules
  now pick up an interval or enabled/disabled change within 30 seconds
  instead of only on the next already-in-flight wait, matching how
  scheduled scrapes already behaved.

### Changed

- Replaced the "Stalled · no seeds" checkbox in Download Activity with a
  dedicated Stalled tab between Completed and Failed.

## [1.0.12] - 2026-08-26

### Added

- Added a taller Release Details screenshot rail with inline navigation and
  middle-button drag scrolling, plus a centered fullscreen screenshot viewer
  with a blurred backdrop, navigable thumbnail strip, current-image highlight,
  keyboard navigation, and click-outside dismissal.

### Fixed

- Centered and correctly scaled the fullscreen screenshot viewer instead of
  anchoring its image at the top-left.
- Prevented the Release Details screenshot rail from being clipped along its
  bottom edge in fullscreen mode.
- Failed cover-hover screenshot requests are no longer cached for the entire
  session, allowing a later hover to retry normally.

## [1.0.11] - 2026-08-26

### Fixed

- Fixed a Firefox startup failure caused by relying on implicit DOM globals for
  screenshot lightbox navigation. The Release Library, navigation, statistics,
  and version badge now finish loading normally.
- Ensured the complete application version remains readable beside Sign out at
  increased interface zoom levels.

## [1.0.10] - 2026-08-26

### Fixed

- Only show locally cached screenshots on card hover and in Release Details;
  viewing the library no longer performs implicit screenshot downloads, and
  releases without local screenshots reserve no carousel space.
- Report the historical screenshot job's exact JavLibrary release total and
  repair missing cache files even when an earlier backfill was completed.

## [1.0.9] - 2026-08-26

### Added

- Added a global, per-user interface zoom setting and a configurable hover
  slideshow interval, avoiding the need to change browser zoom for readability.
- Extract and locally cache full-size JavLibrary preview screenshots under the
  separate `/app/data/screenshots` path.
- Added screenshot slideshows on cover hover and a compact Release Details
  carousel that disappears completely when no screenshots exist.
- Added a natural-size screenshot lightbox with Escape/mouse-out closing and
  navigation through the configured Release Details previous/next shortcuts.
- Added resumable historical screenshot maintenance that works newest-first,
  remembers releases with or without screenshots, and queues each scrape at a
  configurable default priority of 75 so normal scrape work can run first.
- Store the human-facing torrent detail page used for a download and expose it
  as a direct link in Download Activity.

### Fixed

- Replaced the oversized Refresh modes information control with a compact,
  properly aligned icon that no longer inherits full-width form-button styles.

## [1.0.8] - 2026-08-25

### Added

- Show a polished one-time, installation-wide changelog after an upgrade,
  containing every release between the previously installed version and the
  new version, grouped clearly by version and change category. Fresh
  installations establish their version baseline without showing an upgrade
  notice.

## [1.0.7] - 2026-08-25

### Added

- Added a paginated Job History that combines scraping and downloading jobs,
  identifies each job category, and shows run time, completion time, status,
  and result details.
- Added notification sorting by download date, download-started time, local
  availability time, notification date, and release date, with sensible
  defaults for each notification tab.
- Added a compact information tooltip explaining the New releases only, Quick
  refresh, and Full refresh modes.

### Changed

- Replaced browser confirmation popups for forced and replacement downloads
  with an in-app confirmation dialog. Successful acceptance now closes the
  search window and refreshes Download Activity.
- Removed Name and Release ID from notification sorting and alphabetized the
  remaining choices.
- Normalized every JavLibrary URL to HTTPS at storage and scrape boundaries,
  preventing HTTP-to-HTTPS redirect races in Byparr/FlareSolverr.

### Fixed

- Kept toast notifications above the Video Details overlay.
- Corrected release timestamps so Added records database insertion time and
  Updated advances only when release data changes. Existing records with an
  Updated timestamp earlier than Added are repaired at startup.

## [1.0.6] - 2026-08-25

### Added

- Added configurable schedule start times, days of the week, and optional cron
  expressions for scheduled scraping.
- Persisted the download monitor's last run and added recent-run history with
  found/downloaded result totals.
- Added Download Activity sorting by seed count and filters for stalled
  downloads, last-seen-complete date, and the Never state.
- Added bulk deletion with an optional automatic replacement search that can
  ignore non-preferred filename rules, selects the result with the most seeds,
  and reports downloaded/not-found totals.

### Changed

- Standardized priority semantics everywhere: `1` is highest priority and
  `999` is lowest priority.
- Set scheduled scrape defaults to New releases only `15`, Quick refresh `16`,
  and Full refresh `17`. New releases only and Quick refresh are enabled on new
  installations; Full refresh is disabled.

## [1.0.5] - 2026-08-25

### Added

- Redesigned Notifications with the Release Library cover-card view, filters,
  sorting, persisted cover scale, and click-through Release Details navigation.

### Changed

- Retained notification search, multi-selection, search-and-download, and clear
  actions in the redesigned view.
- Removed the redundant All category filter from Notifications.

## [1.0.4] - 2026-08-25

### Added

- Added Play Count to structured release filters and clearly identified fields
  supplied by StashApp.

### Changed

- Refined the sidebar version badge and enlarged the missing-cover text while
  reducing and lowering its illustration.

### Fixed

- Fixed the initial Stash sync so O Count, Play Count, Last Played, and Last O
  Count are populated immediately after a release is matched to a Stash scene.

## [1.0.3] - 2026-08-25

### Added

- Added PostgreSQL 18 to the example stack with a fixed `PGDATA=/pgdata` and a
  configurable `POSTGRES_DATA_PATH` mounted directly as the cluster directory.
- Added configurable JAVBeacon listen and application-data paths.

### Changed

- Simplified the unused cover-path configuration.
- Enlarged the branded missing-cover placeholder for library cards.

## [1.0.2] - 2026-08-25

### Added

- Added an out-of-the-box Docker Compose stack containing JAVBeacon,
  PostgreSQL, and Byparr.
- Added the PostgreSQL Large Library / SSD tuning profile and configurable
  database credentials and storage paths.
- Added Byparr configuration and setup documentation, with fresh installs
  defaulting JavLibrary requests to the Compose service.
- Added multi-architecture GHCR package publishing and provenance attestations
  to the version-tag release workflow.

## [1.0.1] - 2026-08-25

### Added

- Added category-specific JavLibrary URL placeholders when creating Actress,
  Director, Maker, Label, and Tag/Genre monitoring sites.
- Added guidance that JavLibrary listing URLs must include `&mode=2` to include
  future releases.

## [1.0.0] - 2026-08-25

### Added

- Published the first versioned JAVBeacon release with frontend version display,
  downloadable binaries, Docker Compose examples, and automated GitHub releases.
- Added a branded dark missing-cover design for releases whose artwork is not
  yet available.

### Changed

- Quick and Full refreshes now check for release cover/logo changes.

### Fixed

- Detect and replace JavLibrary's temporary `NOW PRINTING` artwork instead of
  caching or displaying it as a real release cover.

[Unreleased]: https://github.com/Net005/JAVBeacon/compare/v1.0.54...HEAD
[1.0.54]: https://github.com/Net005/JAVBeacon/compare/v1.0.53...v1.0.54
[1.0.53]: https://github.com/Net005/JAVBeacon/compare/v1.0.52...v1.0.53
[1.0.52]: https://github.com/Net005/JAVBeacon/compare/v1.0.51...v1.0.52
[1.0.51]: https://github.com/Net005/JAVBeacon/compare/v1.0.50...v1.0.51
[1.0.50]: https://github.com/Net005/JAVBeacon/compare/v1.0.49...v1.0.50
[1.0.49]: https://github.com/Net005/JAVBeacon/compare/v1.0.48...v1.0.49
[1.0.48]: https://github.com/Net005/JAVBeacon/compare/v1.0.47...v1.0.48
[1.0.47]: https://github.com/Net005/JAVBeacon/compare/v1.0.46...v1.0.47
[1.0.46]: https://github.com/Net005/JAVBeacon/compare/v1.0.45...v1.0.46
[1.0.45]: https://github.com/Net005/JAVBeacon/compare/v1.0.44...v1.0.45
[1.0.44]: https://github.com/Net005/JAVBeacon/compare/v1.0.43...v1.0.44
[1.0.43]: https://github.com/Net005/JAVBeacon/compare/v1.0.42...v1.0.43
[1.0.42]: https://github.com/Net005/JAVBeacon/compare/v1.0.41...v1.0.42
[1.0.41]: https://github.com/Net005/JAVBeacon/compare/v1.0.40...v1.0.41
[1.0.40]: https://github.com/Net005/JAVBeacon/compare/v1.0.39...v1.0.40
[1.0.39]: https://github.com/Net005/JAVBeacon/compare/v1.0.38...v1.0.39
[1.0.38]: https://github.com/Net005/JAVBeacon/compare/v1.0.37...v1.0.38
[1.0.37]: https://github.com/Net005/JAVBeacon/compare/v1.0.36...v1.0.37
[1.0.36]: https://github.com/Net005/JAVBeacon/compare/v1.0.35...v1.0.36
[1.0.35]: https://github.com/Net005/JAVBeacon/compare/v1.0.34...v1.0.35
[1.0.34]: https://github.com/Net005/JAVBeacon/compare/v1.0.33...v1.0.34
[1.0.33]: https://github.com/Net005/JAVBeacon/compare/v1.0.32...v1.0.33
[1.0.32]: https://github.com/Net005/JAVBeacon/compare/v1.0.31...v1.0.32
[1.0.31]: https://github.com/Net005/JAVBeacon/compare/v1.0.30...v1.0.31
[1.0.30]: https://github.com/Net005/JAVBeacon/compare/v1.0.29...v1.0.30
[1.0.29]: https://github.com/Net005/JAVBeacon/compare/v1.0.28...v1.0.29
[1.0.28]: https://github.com/Net005/JAVBeacon/compare/v1.0.27...v1.0.28
[1.0.27]: https://github.com/Net005/JAVBeacon/compare/v1.0.26...v1.0.27
[1.0.26]: https://github.com/Net005/JAVBeacon/compare/v1.0.25...v1.0.26
[1.0.25]: https://github.com/Net005/JAVBeacon/compare/v1.0.24...v1.0.25
[1.0.24]: https://github.com/Net005/JAVBeacon/compare/v1.0.23...v1.0.24
[1.0.23]: https://github.com/Net005/JAVBeacon/compare/v1.0.22...v1.0.23
[1.0.22]: https://github.com/Net005/JAVBeacon/compare/v1.0.21...v1.0.22
[1.0.21]: https://github.com/Net005/JAVBeacon/compare/v1.0.20...v1.0.21
[1.0.20]: https://github.com/Net005/JAVBeacon/compare/v1.0.19...v1.0.20
[1.0.19]: https://github.com/Net005/JAVBeacon/compare/v1.0.18...v1.0.19
[1.0.18]: https://github.com/Net005/JAVBeacon/compare/v1.0.17...v1.0.18
[1.0.17]: https://github.com/Net005/JAVBeacon/compare/v1.0.16...v1.0.17
[1.0.16]: https://github.com/Net005/JAVBeacon/compare/v1.0.15...v1.0.16
[1.0.15]: https://github.com/Net005/JAVBeacon/compare/v1.0.14...v1.0.15
[1.0.14]: https://github.com/Net005/JAVBeacon/compare/v1.0.13...v1.0.14
[1.0.13]: https://github.com/Net005/JAVBeacon/compare/v1.0.12...v1.0.13
[1.0.12]: https://github.com/Net005/JAVBeacon/compare/v1.0.11...v1.0.12
[1.0.11]: https://github.com/Net005/JAVBeacon/compare/v1.0.10...v1.0.11
[1.0.10]: https://github.com/Net005/JAVBeacon/compare/v1.0.9...v1.0.10
[1.0.9]: https://github.com/Net005/JAVBeacon/compare/v1.0.8...v1.0.9
[1.0.8]: https://github.com/Net005/JAVBeacon/compare/v1.0.7...v1.0.8
[1.0.7]: https://github.com/Net005/JAVBeacon/compare/v1.0.6...v1.0.7
[1.0.6]: https://github.com/Net005/JAVBeacon/compare/v1.0.5...v1.0.6
[1.0.5]: https://github.com/Net005/JAVBeacon/compare/v1.0.4...v1.0.5
[1.0.4]: https://github.com/Net005/JAVBeacon/compare/v1.0.3...v1.0.4
[1.0.3]: https://github.com/Net005/JAVBeacon/compare/v1.0.2...v1.0.3
[1.0.2]: https://github.com/Net005/JAVBeacon/compare/v1.0.1...v1.0.2
[1.0.1]: https://github.com/Net005/JAVBeacon/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/Net005/JAVBeacon/releases/tag/v1.0.0
