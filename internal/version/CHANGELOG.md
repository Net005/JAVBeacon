# Changelog

All notable user-facing changes to JAVBeacon are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and JAVBeacon uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.0.12] - 2026-08-26

### Added

- Added a taller Release Details screenshot rail with inline navigation and
  middle-button drag scrolling, plus a centered fullscreen screenshot viewer
  with a blurred backdrop, navigable thumbnail strip, current-image highlight,
  keyboard navigation, and click-outside dismissal.

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

[Unreleased]: https://github.com/Net005/JAVBeacon/compare/v1.0.12...HEAD
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
