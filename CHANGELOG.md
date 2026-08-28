# Changelog

All notable user-facing changes to JAVBeacon are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and JAVBeacon uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/Net005/JAVBeacon/compare/v1.0.28...HEAD
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
