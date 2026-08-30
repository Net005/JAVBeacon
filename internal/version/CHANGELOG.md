# Changelog

All notable user-facing changes to JAVBeacon are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and JAVBeacon uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
  iPhone and iPad, shared across All, Released, Upcoming, Local, and Desired so
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
  sort (newest first), and Desired defaults to a new "Marked Desired" sort
  (newest first) tracking when each release was most recently marked
  desired. Changing the sort on one tab no longer affects the others.
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

- StashApp sync's Local library sync and Desired-tag sync now show their own
  enabled/interval state and next 3 predicted run times directly under
  Settings → StashApp's own controls, the same way Settings → Scraping does
  for the scrape schedules.
- The Release Library's Released tab now has a configurable "Min days since
  release" / "Max days since release" window, remembered across visits. The
  tab always shows the effective date range applied ("Showing releases
  released between X and Y", or "...released up to X" with no minimum set),
  computed from those two settings.
- The card "Actions" menu (Search, Search & Download, Update details, Open
  detail) moved from its own full-width row into the Notify/Desired/Monitor
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
  library and Desired-tag sync.
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

[Unreleased]: https://github.com/Net005/JAVBeacon/compare/v1.0.49...HEAD
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
