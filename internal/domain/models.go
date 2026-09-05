package domain

import (
	"encoding/json"
	"strings"
	"time"
)

type Site struct {
	ID                int64  `json:"id"`
	Title             string `json:"title"`
	Type              string `json:"type"`
	Name              string `json:"name"`
	URL               string `json:"url"`
	Notify            bool   `json:"notify"`
	AutoMonitorFuture bool   `json:"auto_monitor_future"`
	// Retained only as compile-time compatibility for extensions built
	// against the old Go model. SaveSite ignores these fields and the API does
	// not serialize them.
	Download          bool      `json:"-"`
	DownloadMode      string    `json:"-"`
	Watchlist         bool      `json:"watchlist"`
	RSSURL            string    `json:"rss_url"`
	Enabled           bool      `json:"enabled"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	LastScrapedAt     time.Time `json:"last_scraped_at,omitempty"`
	LastScrapePages   int       `json:"last_scrape_pages"`
	LastScrapeAdded   int       `json:"last_scrape_added"`
	LastScrapeUpdated int       `json:"last_scrape_updated"`
	LastScrapeState   string    `json:"last_scrape_state,omitempty"`
}

type Release struct {
	ID                  int64    `json:"id"`
	SiteID              int64    `json:"site_id"`
	SiteTitle           string   `json:"site_title"`
	SiteIDs             []int64  `json:"site_ids,omitempty"`
	SiteTitles          []string `json:"site_titles,omitempty"`
	VideoID             string   `json:"video_id"`
	ScraperID           string   `json:"scraper_id"`
	Title               string   `json:"title"`
	ReleaseDate         string   `json:"release_date"`
	Source              string   `json:"source"`
	ImageURL            string   `json:"image_url"`
	ProductURL          string   `json:"product_url"`
	Actress             string   `json:"actress"`
	Actresses           []string `json:"actresses"`
	Director            string   `json:"director"`
	Studio              string   `json:"studio"`
	Label               string   `json:"label"`
	Genres              []string `json:"genres"`
	Duration            string   `json:"duration"`
	Story               string   `json:"story"`
	Screenshots         []string `json:"screenshots"`
	Released            bool     `json:"released"`
	Local               bool     `json:"local"`
	Notified            bool     `json:"notified"`
	NotifyOnRelease     bool     `json:"notify_on_release"`
	Watchlist           bool     `json:"watchlist"`
	MonitorDownload     bool     `json:"monitor_download"`
	MonitorReason       string   `json:"monitor_reason,omitempty"`
	MonitorSiteID       int64    `json:"monitor_site_id,omitempty"`
	MonitorSiteTitle    string   `json:"monitor_site_title,omitempty"`
	SiteMonitorDownload bool     `json:"-"`
	StashSceneID        string   `json:"stash_scene_id"`
	// StashFilePath is the complete path of the first video file attached to
	// the matched StashApp scene. It is synchronized with local availability
	// and exposed through the release API for consumers such as JAVBeaconSubs.
	StashFilePath string `json:"stash_file_path,omitempty"`
	WatchlistSync string `json:"watchlist_sync,omitempty"`
	// DownloadStatus is computed: "downloading" when an active torrent
	// exists for this release, "completed" when the most recent finished
	// download for it succeeded, or "" when there is none (Phase 6A).
	DownloadStatus string `json:"download_status,omitempty"`
	// DownloadSourceReference is the torrent detail-page URL belonging to the
	// same download row selected for DownloadStatus. It lets every status pill
	// open the exact external result that is downloading or was downloaded.
	DownloadSourceReference string `json:"download_source_reference,omitempty"`
	// Download telemetry belongs to the same active/completed download row as
	// DownloadStatus and is populated for the single-release detail endpoint.
	// Keeping it out of the library-card query avoids extra work across large
	// result sets while still giving Release Details current qBittorrent data.
	DownloadSeeds        int       `json:"download_seeds"`
	DownloadPeers        int       `json:"download_peers"`
	DownloadTransport    string    `json:"download_transport,omitempty"`
	DownloadBytesTotal   int64     `json:"download_bytes_total,omitempty"`
	DownloadBytesDone    int64     `json:"download_bytes_downloaded,omitempty"`
	DownloadETASeconds   int64     `json:"download_eta_seconds"`
	DownloadSeenComplete int64     `json:"download_seen_complete"`
	DownloadAddedAt      time.Time `json:"download_added_at,omitempty"`
	// DownloadedAt is computed, mirroring DownloadStatus: the completion
	// timestamp of the most recent successful download, or the zero value
	// when this release has never finished downloading (TODO-2.0 card/detail
	// "Downloaded (with date)").
	DownloadedAt time.Time `json:"downloaded_at,omitempty"`
	// StashAddedAt is set once, the first time StashSceneID transitions from
	// empty to non-empty during a StashApp local-library sync, and is left
	// unchanged on every later sync of the same release (TODO-2.0 card/detail
	// "Added to StashApp (with date)").
	StashAddedAt time.Time `json:"stash_added_at,omitempty"`
	// StashCreatedAt is StashApp's own "Created At" timestamp for this
	// release's matched scene, as opposed to StashAddedAt (JAVBeacon's own
	// bookkeeping for when it first noticed the match). Best-effort,
	// populated during the same scene-ID-keyed secondary sync pass as
	// OCounter/PlayCount/LastPlayedAt (see stash.Service.run and
	// SetStashCreatedAt), so a StashApp version or custom
	// stash_graphql_query that can't provide created_at just leaves this
	// blank rather than failing the whole sync. Powers the Release
	// Library's Local-tab "Added Locally" sort (local_added), which
	// reflects when the scene was actually created in your StashApp
	// library rather than when JAVBeacon happened to notice it.
	StashCreatedAt time.Time `json:"stash_created_at,omitempty"`
	// WatchlistAt records when a release was (most recently) marked Watchlist:
	// refreshed on every PatchRelease call that sets Watchlist to true (unlike
	// StashAddedAt's "set once" pattern, re-marking something Watchlist after
	// unmarking it is a deliberate user action that should bubble it back to
	// the top), and left untouched when Watchlist is cleared. It powers the
	// Release Library's "Watchlist" tab default sort (newest marked first) via
	// the watchlist_marked sort option.
	WatchlistAt time.Time `json:"watchlist_at,omitempty"`
	// StashReleaseDate is the release's `date` field as recorded on its
	// matched StashApp scene, kept in sync opportunistically during a local-
	// library sync. It exists to fill in a release date for display when
	// ReleaseDate is blank (TODO-2.0's "Missing released status display");
	// it does not feed the released/monitored eligibility computation, which
	// stays governed by ReleaseDate and a download-site match as before.
	StashReleaseDate string `json:"stash_release_date,omitempty"`
	// AllowNonPreferredFilenames persists the Missing Library Files "allow
	// non-preferred filenames" override onto the release itself, rather
	// than letting it live only as a one-off flag on a single apply run.
	// Once set (whether from that apply flow or a manual bulk toggle on
	// the "Releases checked by the scheduled job" table), the scheduled
	// download-search job's per-release match in runSearch uses the same
	// relaxed fallbackSearchCandidate chain SearchAndDownloadNow does
	// instead of requiring a normal accepted-filename-pattern match, so a
	// release that needed the relaxed rule once keeps getting it on every
	// future scheduled check too.
	AllowNonPreferredFilenames bool `json:"allow_non_preferred_filenames"`
	// IgnoreLocalForceDownload persists a per-release override that makes
	// download.Service treat this release as needing a download even
	// though it already has a matched StashApp scene (Local/StashSceneID
	// set) - the normal rule (download.Service.duplicate) otherwise skips
	// any release already linked in StashApp, on the assumption its file
	// already exists. That assumption is wrong for a release recovered
	// through Missing Library Files: the scene is linked, but its actual
	// file on disk is what's missing, so runApply sets this flag
	// automatically whenever it marks such a release monitored. It can
	// also be set directly (or via the "Monitored releases" bulk actions)
	// for any other release whose local copy is known bad/gone despite
	// still being linked in StashApp.
	IgnoreLocalForceDownload bool `json:"ignore_local_force_download"`
	// HTTPDownloadPrimary makes the JavDB/Keepshare HTTP provider the first
	// choice for automatic Search + Download attempts on this release. The
	// default is false: qBittorrent remains primary and HTTP is a fallback.
	HTTPDownloadPrimary bool `json:"http_download_primary"`
	// OCounter, PlayCount, LastPlayedAt, and LastOCountAt mirror
	// StashMissingScene's identically-named/purposed fields (stash_missing.go)
	// but for a release already matched to a StashApp scene: best-effort,
	// populated opportunistically by stash.Service.run's playback-stats pass
	// alongside StashReleaseDate, and never cleared by a sync round that
	// can't determine a fresh value (see SetStashPlaybackStats). LastOCountAt
	// is derived from StashApp's o_history list (its most recent entry) since
	// StashApp has no single "last O count" scalar field of its own.
	OCounter     int       `json:"o_counter,omitempty"`
	PlayCount    int       `json:"play_count,omitempty"`
	LastPlayedAt string    `json:"last_played_at,omitempty"`
	LastOCountAt string    `json:"last_o_count_at,omitempty"`
	AddedAt      time.Time `json:"added_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// ReleaseEdit holds every release field the Metadata Edit feature (manual
// correction of any release's data, including its own bookkeeping dates)
// allows a user to change by hand. Every field is a pointer so a partial
// edit only touches what the form actually submitted - nil means "leave
// this alone", not "clear it".
//
// Deliberately excluded: Local, Notified, StashSceneID, StashFilePath,
// StashAddedAt, StashCreatedAt, StashReleaseDate, WatchlistAt, and
// DownloadedAt. Those are derived from real external state (an actual
// download, an actual StashApp sync) rather than being facts about the
// release itself, so hand-editing them here would let the UI claim something
// happened (a download, a library sync) that didn't -
// they stay system-managed, updated only by the code paths that actually
// perform those actions.
//
// UpdatedAt is the one field worth calling out specially: when left nil it
// still defaults to "now" (an edit is itself an update), but the field
// exists so a deliberate backdate is also possible, same as AddedAt.
type ReleaseEdit struct {
	Title           *string    `json:"title"`
	ReleaseDate     *string    `json:"release_date"`
	Studio          *string    `json:"studio"`
	Label           *string    `json:"label"`
	Director        *string    `json:"director"`
	Duration        *string    `json:"duration"`
	Story           *string    `json:"story"`
	Actress         *string    `json:"actress"`
	Genres          *[]string  `json:"genres"`
	Released        *bool      `json:"released"`
	Watchlist       *bool      `json:"watchlist"`
	MonitorDownload *bool      `json:"monitor_download"`
	NotifyOnRelease *bool      `json:"notify_on_release"`
	AddedAt         *time.Time `json:"added_at"`
	UpdatedAt       *time.Time `json:"updated_at"`
}

type ReleaseFilter struct {
	Search, Site, Source, Status, Sort, Direction, Category, Entries, SearchExpression string
	// VideoID, when set, restricts results to an exact (case-insensitive)
	// video_id match instead of the fuzzy substring matching Search does.
	// Used for lookups that must resolve to a single specific release - for
	// example the community StashApp JavLibrary scraper's optional
	// JAVBeacon-backed mode, which looks a scene up by its exact DVD code
	// before falling back to scraping JavLibrary directly.
	VideoID string
	// StashFilePath, when set, restricts results to an exact, case-insensitive
	// full video path reported by StashApp. This lets API clients such as
	// JAVBeaconSubs map a media file back to its JAVBeacon release without a
	// fuzzy text match.
	StashFilePath                                       string
	SiteID                                              int64
	Watchlist, HideLocal, MonitorDownload, UsePreferred bool
	Limit, Offset                                       int
	// ShowNonPreferred, when false (the default), tells Releases/ReleasesCount
	// to exclude any release matching an ignore rule (see IgnoreTags and
	// IgnoreTitles) - the Release Library's "hide ignored releases" behavior.
	// The frontend's "Show non-preferred" toggle sets this true to reveal
	// them again. IgnoreTags/IgnoreTitles are populated by the HTTP handler
	// from the ignore_tags/ignore_titles settings (only when
	// ShowNonPreferred is false, since there is nothing to filter by
	// otherwise) rather than looked up again inside the store layer, so
	// Releases/ReleasesCount never need their own settings access.
	ShowNonPreferred         bool
	IgnoreTags, IgnoreTitles []string
	// AllowNonPreferredFilenames filters the "Releases checked by the
	// scheduled job" table by the persistent per-release override of the
	// same name: nil means no filter, true/false restrict to releases with
	// the flag set/unset.
	AllowNonPreferredFilenames *bool
	// IgnoreLocalForceDownload filters the "Releases checked by the
	// scheduled job" table by the persistent per-release override of the
	// same name: nil means no filter, true/false restrict to releases with
	// the flag set/unset.
	IgnoreLocalForceDownload *bool
	// MinReleaseDate/MaxReleaseDate restrict r.release_date to an inclusive
	// "YYYY-MM-DD" range when set (empty means unbounded on that side). The
	// Release Library's Released tab uses this for its configurable
	// min/max-days-released window, so a release whose release_date hasn't
	// actually arrived yet (a data mismatch with the released flag) can be
	// excluded even though r.released=1.
	MinReleaseDate, MaxReleaseDate string
}

// ReleasePage is the cursor-paginated, lightweight result used by the
// Release Library cover grid. Other release consumers keep using Releases,
// which returns full rows and offset pagination for backwards compatibility.
type ReleasePage struct {
	Items      []Release `json:"items"`
	NextCursor string    `json:"next_cursor,omitempty"`
	NextOffset int       `json:"next_offset,omitempty"`
}

// ParseIgnoreList splits a newline-separated ignore_tags or ignore_titles
// setting value into trimmed, non-empty entries, one per line.
//
// This is deliberately newline-only, not newline-or-comma like the
// Downloads settings panel's accepted_patterns field: a real StashApp/
// JavLibrary tag or genre can itself contain a comma (e.g. "Best,
// Omnibus"), and splitting on commas made that tag impossible to enter
// correctly - "Best, Omnibus" silently became two separate ignore entries
// ("Best" and "Omnibus") that never matched the actual genre string. A tag
// containing a space (e.g. "Big Tits") was never affected by this bug -
// only literal commas inside a tag name were - but one-per-line avoids the
// ambiguity entirely rather than special-casing it.
func ParseIgnoreList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, "\n") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// DownloadFilter is the paginated/filterable query used by the Download
// Activity table (Phase 4B). Search matches release video ID, the search
// query, or the torrent name; Source matches provider or source type.
type DownloadFilter struct {
	Status, Search, Source, Transport, Sort, Direction string
	Limit, Offset                                      int
	// FilenamePatternExcluded, when true, restricts DownloadActivity to
	// downloads submitted despite not being a normal accepted-filename
	// match (TODO-2.0 Task A) - see Download.FilenamePatternExcluded. Like
	// ReleaseFilter's other one-way toggles (Watchlist, HideLocal, ...),
	// false means "don't filter on this" rather than "must be false".
	FilenamePatternExcluded bool
	Stalled                 bool
	SeenComplete            string
	SeenCompleteDate        int64
}

// StashMissingScene is one StashApp scene whose file(s) could not be found
// on disk during a "Missing Library Files" scan (TODO-2.0 Phase 2). It
// mirrors the fields fetched from StashApp's GraphQL API plus the
// recovery-workflow state JAVBeacon layers on top: an optional link to
// a JAVBeacon Release (once one has been matched or retrieved) and a
// status tracking that recovery workflow specifically (missing/retrieving/
// retrieve_failed) - once ReleaseID is set, the release's own download
// status (via ReleaseDownloadStatus/ReleaseMonitorDownload, computed by a
// join rather than duplicated here) is what actually drives "downloading" /
// "downloaded" / "failed" in the UI, so this type never needs to track that
// itself.
type StashMissingScene struct {
	ID            int64    `json:"id"`
	StashSceneID  string   `json:"stash_scene_id"`
	Title         string   `json:"title"`
	Code          string   `json:"code"`
	Date          string   `json:"date"`
	Path          string   `json:"path"`
	Paths         []string `json:"paths"`
	OCounter      int      `json:"o_counter"`
	PlayCount     int      `json:"play_count"`
	LastPlayedAt  string   `json:"last_played_at"`
	LastOCountAt  string   `json:"last_o_count_at"`
	Studio        string   `json:"studio"`
	Tags          []string `json:"tags"`
	URLs          []string `json:"urls"`
	JavLibraryURL string   `json:"javlibrary_url"`

	// ReleaseID is 0 until a JAVBeacon release has been matched (by
	// video ID) or retrieved (scraped from JavLibrary) for this scene.
	ReleaseID              int64  `json:"release_id,omitempty"`
	ReleaseVideoID         string `json:"release_video_id,omitempty"`
	ReleaseTitle           string `json:"release_title,omitempty"`
	ReleaseMonitorDownload bool   `json:"release_monitor_download,omitempty"`

	// Status is one of: missing, retrieving, retrieve_failed. It only
	// governs the pre-release-link recovery workflow; see the type comment.
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`

	// EffectiveStatus is computed by the store at read time: missing,
	// retrieving, retrieve_failed, monitored, searching, downloading,
	// downloaded, or failed - folding in the linked release's live download
	// state so the UI has one field to filter/display on.
	EffectiveStatus string `json:"effective_status"`

	FirstSeenAt time.Time `json:"first_seen_at"`
	LastScanAt  time.Time `json:"last_scan_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// StashMissingFilter is the paginated/filterable query used by the Missing
// Library Files section. SearchExpression uses the same {"logic":
// "and"|"or", "conditions": [...]} JSON shape as ReleaseFilter's Conditions
// builder, but against a different field set - see
// stashMissingFilterWhere in internal/store.
type StashMissingFilter struct {
	Status, SearchExpression, Sort, Direction string
	Limit, Offset                             int
}

type Stats struct {
	Sites    int `json:"sites"`
	Releases int `json:"releases"`
	Released int `json:"released"`
	Upcoming int `json:"upcoming"`
	Local    int `json:"local"`
}
type QueuedJob struct {
	Position  int    `json:"position"`
	SiteID    int64  `json:"site_id,omitempty"`
	ReleaseID int64  `json:"release_id,omitempty"`
	Title     string `json:"title"`
	Mode      string `json:"mode,omitempty"`
	Priority  int    `json:"priority"`
	AllPages  bool   `json:"all_pages,omitempty"`
	Scheduled bool   `json:"scheduled,omitempty"`
}
type Job struct {
	ID              int64     `json:"id,omitempty"`
	Kind            string    `json:"kind,omitempty"`
	Title           string    `json:"title,omitempty"`
	Scheduled       bool      `json:"scheduled,omitempty"`
	State           string    `json:"state"`
	Mode            string    `json:"mode,omitempty"`
	Running         bool      `json:"running"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	FinishedAt      time.Time `json:"finished_at,omitempty"`
	Added           int       `json:"added"`
	Updated         int       `json:"updated"`
	Skipped         int       `json:"skipped"`
	SiteTitle       string    `json:"site_title,omitempty"`
	Provider        string    `json:"provider,omitempty"`
	Page            int       `json:"page,omitempty"`
	PageLimit       int       `json:"page_limit,omitempty"`
	PageLimitSource string    `json:"page_limit_source,omitempty"`
	AllPages        bool      `json:"all_pages,omitempty"`
	// SiteIndex and SiteCount report a scrape job's progress across the
	// enabled monitoring sites it's working through - SiteIndex is the
	// 1-based position of SiteTitle within this job's site list, SiteCount
	// the total sites the job resolved to scan when it started. Both are
	// only set for a job scanning every enabled site (RefreshOptions.SiteID
	// == 0, i.e. any scheduled Quick/Full/New refresh or a manual "all
	// sites" run) - a single-site job leaves them zero, since "1 of 1" adds
	// nothing a progress bar needs.
	SiteIndex  int         `json:"site_index,omitempty"`
	SiteCount  int         `json:"site_count,omitempty"`
	Priority   int         `json:"priority,omitempty"`
	QueueDepth int         `json:"queue_depth,omitempty"`
	QueuedJobs []QueuedJob `json:"queued_jobs,omitempty"`
	ReleaseID  int64       `json:"release_id,omitempty"`
	Item       int         `json:"item,omitempty"`
	PageItems  int         `json:"page_items,omitempty"`
	Remaining  int         `json:"remaining,omitempty"`
	VideoID    string      `json:"video_id,omitempty"`
	Error      string      `json:"error,omitempty"`
	// Paused reports that this job has voluntarily yielded the worker to a
	// higher-priority job queued while it was running (see
	// Service.checkpoint) - it is still Running, just not currently making
	// progress. PausedFor names the job it is waiting on (its Title), for
	// display. Both clear back to false/"" once the higher-priority work
	// (and anything else that outranks this job) has drained and this job
	// resumes exactly where it left off.
	Paused    bool   `json:"paused,omitempty"`
	PausedFor string `json:"paused_for,omitempty"`
	// Stage is a Phase 12 live-progress label for a single-release "Update
	// details" job while it is running: one of "connecting",
	// "connecting_flaresolverr", "parsing", "comparing", "updating", or
	// "completed". It is transient (not persisted to job_history) and only
	// meaningful for a release-scoped job.
	Stage string `json:"stage,omitempty"`
	// Outcome is the Phase 12 final result of a release "Update details"
	// job, once it has stopped running: "updated", "no_change", "invalid",
	// "blocked", or "failed".
	Outcome string `json:"outcome,omitempty"`
	// ActiveReleaseJobs lists every release-scoped "Update details" refresh
	// currently running concurrently (via monitor.Service.StartRelease),
	// independent of whatever this Job itself represents (a scan job, or
	// idle). Populated only on the Status()/GET /api/jobs/refresh response,
	// never on an individual entry within the list itself, and never
	// persisted to job_history.
	ActiveReleaseJobs []Job `json:"active_release_jobs,omitempty"`
}

// JobHistoryEntry is one row in the unified Jobs activity timeline. Scrape
// jobs and downloads have different source tables, but share the timestamps
// and display fields needed by the paginated history view.
type JobHistoryEntry struct {
	ID         int64     `json:"id"`
	Category   string    `json:"category"`
	Kind       string    `json:"kind"`
	State      string    `json:"state"`
	Mode       string    `json:"mode,omitempty"`
	Title      string    `json:"title,omitempty"`
	Provider   string    `json:"provider,omitempty"`
	Scheduled  bool      `json:"scheduled,omitempty"`
	SiteCount  int       `json:"site_count,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Added      int       `json:"added"`
	Updated    int       `json:"updated"`
	Skipped    int       `json:"skipped"`
	Error      string    `json:"error,omitempty"`
	Details    string    `json:"details,omitempty"`
}

// HistoricalBackfillSource is one durable JavLibrary graph/index cursor.
// CursorDate is the oldest fully processed release date. On resume the
// crawler starts at the date-sorted head until it reaches this boundary, so
// insertions at the front cannot hide releases behind a stale page number.
type HistoricalBackfillSource struct {
	URL, Kind, Name, State, CursorDate, ResumeDate string
	NextPage, PageLimit, PagesCompleted            int
	CatchupOnly                                    bool
}

type HistoricalBackfillItem struct {
	VideoID, ReleaseDate, State, SourceURL, Error string
}

type HistoricalBackfillStats struct {
	State              string    `json:"state"`
	UpdatedAt          time.Time `json:"updated_at,omitempty"`
	SourcesTotal       int       `json:"sources_total"`
	SourcesCompleted   int       `json:"sources_completed"`
	PagesCompleted     int       `json:"pages_completed"`
	PagesEstimated     int       `json:"pages_estimated"`
	ReleasesDiscovered int       `json:"releases_discovered"`
	ReleasesCompleted  int       `json:"releases_completed"`
	ReleasesFailed     int       `json:"releases_failed"`
}

type User struct {
	ID           int64
	Username     string
	PasswordHash string
}

type Session struct {
	Token     string
	UserID    int64
	ExpiresAt time.Time
}

type FilterPreset struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	State     json.RawMessage `json:"state"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type Download struct {
	ID              int64           `json:"id"`
	ReleaseID       int64           `json:"release_id"`
	VideoID         string          `json:"video_id,omitempty"`
	ImageURL        string          `json:"image_url,omitempty"`
	Provider        string          `json:"provider"`
	SourceType      string          `json:"source_type"`
	SourceReference string          `json:"source_reference"`
	SourcePageURL   string          `json:"source_page_url,omitempty"`
	Query           string          `json:"query"`
	TorrentHash     string          `json:"torrent_hash"`
	Transport       string          `json:"transport"`
	DestinationPath string          `json:"destination_path,omitempty"`
	BytesTotal      int64           `json:"bytes_total,omitempty"`
	BytesDownloaded int64           `json:"bytes_downloaded,omitempty"`
	BytesPerSecond  int64           `json:"bytes_per_second,omitempty"`
	Name            string          `json:"name"`
	Files           json.RawMessage `json:"files"`
	Status          string          `json:"status"`
	MatchReason     string          `json:"match_reason"`
	QBResponse      string          `json:"qb_response"`
	PostStatus      string          `json:"post_status"`
	Error           string          `json:"error,omitempty"`
	SeedRatio       float64         `json:"seed_ratio"`
	Progress        float64         `json:"progress"`
	Seeds           int             `json:"seeds"`
	Peers           int             `json:"peers"`
	ETASeconds      int64           `json:"eta_seconds"`
	SeenComplete    int64           `json:"seen_complete"`
	AddedAt         time.Time       `json:"added_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	// FilenamePatternExcluded marks a download that was submitted despite
	// NOT being a normal accepted-filename-pattern match (TODO-2.0 Task A):
	// either a manual "Force download" override (SearchResult.Forced), or
	// the Missing Library Files "allow non-preferred filenames" fallback
	// chain (SearchResult.FilenamePatternExcluded) picking a seeded-but-
	// unaccepted or, failing that, merely most-recent result. Distinct from
	// MatchReason's free text so the Download Activity view can filter on
	// it structurally rather than string-matching a human-readable reason.
	FilenamePatternExcluded bool  `json:"filename_pattern_excluded,omitempty"`
	CanReplace              bool  `json:"can_replace,omitempty"`
	ExistingDownloadID      int64 `json:"existing_download_id,omitempty"`
}

type SearchFile struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	Matched   bool   `json:"matched,omitempty"`
}

type SearchResult struct {
	Provider               string       `json:"provider"`
	Title                  string       `json:"title"`
	Files                  []string     `json:"files,omitempty"`
	FileDetails            []SearchFile `json:"file_details,omitempty"`
	MatchedFile            string       `json:"matched_file,omitempty"`
	PreferredFilenameMatch bool         `json:"preferred_filename_match,omitempty"`
	// Link is the magnet/.torrent URL actually submitted to qBittorrent.
	Link string `json:"link"`
	// SourceURL is the human-facing torrent detail page (e.g. a Sukebei/Nyaa
	// /view/<id> page), when the provider exposed one. It is separate from
	// Link so the UI can offer "open the source page" without that click
	// being mistaken for submitting the download.
	SourceURL string `json:"source_url,omitempty"`
	// Transport is "torrent" for qBittorrent results and "http" for a
	// JavDB/Keepshare direct-download candidate.
	Transport   string `json:"transport,omitempty"`
	SizeBytes   int64  `json:"size_bytes,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
	Accepted    bool   `json:"accepted"`
	Reason      string `json:"reason"`
	// Forced marks a result that was downloaded via an explicit manual
	// override after automatic matching rejected it (Phase 5B), so the
	// resulting Download's history clearly shows it was not an automatic
	// accept.
	Forced bool `json:"forced,omitempty"`
	// IgnoreLocal is a one-shot manual override of the StashApp-local
	// duplicate guard. It is deliberately separate from Forced: an otherwise
	// preferred result can be a valid redownload without being mislabeled as
	// a filename-rule exception in Download Activity.
	IgnoreLocal bool `json:"ignore_local,omitempty"`
	// FilenamePatternExcluded marks a result selected by the Missing
	// Library Files "allow non-preferred filenames" fallback chain
	// (TODO-2.0 Task A) rather than a normal accepted-pattern match -
	// Service.Download folds this into domain.Download.
	// FilenamePatternExcluded the same way it folds in Forced, so both
	// paths land on the same structured, filterable flag.
	FilenamePatternExcluded bool `json:"filename_pattern_excluded,omitempty"`
	// ReplaceExisting is accepted only on an explicit retry after the manual
	// search UI has shown and confirmed an active-download conflict.
	ReplaceExisting bool `json:"replace_existing,omitempty"`
	// Seeds and Peers are the torrent's current seeder/leecher counts as
	// reported by the search provider's listing, so a user picking between
	// several results in the Search & Download window can see which one is
	// actually likely to complete before committing to it. A handful of
	// results have no listing metadata to read this from (e.g. a torrent
	// resolved only from a raw HTML link with no RSS entry) - both are left
	// at 0 in that case, same as a genuinely dead torrent.
	Seeds int    `json:"seeds"`
	Peers int    `json:"peers"`
	Size  string `json:"size,omitempty"`
}

type DownloadSearchJob struct {
	Running    bool      `json:"running"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Checked    int       `json:"checked"`
	Found      int       `json:"found"`
	Downloaded int       `json:"downloaded"`
	Skipped    int       `json:"skipped"`
	Failed     int       `json:"failed"`
	VideoID    string    `json:"video_id,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// DownloadReplacementJob tracks one bulk Download Activity cleanup and its
// optional best-seeded replacement searches.
type DownloadReplacementJob struct {
	Running      bool      `json:"running"`
	Replace      bool      `json:"replace"`
	NonPreferred bool      `json:"non_preferred"`
	StartedAt    time.Time `json:"started_at,omitempty"`
	FinishedAt   time.Time `json:"finished_at,omitempty"`
	Total        int       `json:"total"`
	Processed    int       `json:"processed"`
	Removed      int       `json:"removed"`
	Downloaded   int       `json:"downloaded"`
	NotFound     int       `json:"not_found"`
	Failed       int       `json:"failed"`
	CurrentItem  string    `json:"current_item,omitempty"`
	LastError    string    `json:"last_error,omitempty"`
}

// DownloadSearchRun is the persisted result of one recent- or older-release
// monitoring pass. It restores "last run" after restart and backs the run
// history shown in Download Monitoring.
type DownloadSearchRun struct {
	ID         int64     `json:"id"`
	Schedule   string    `json:"schedule"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Checked    int       `json:"checked"`
	Found      int       `json:"found"`
	Downloaded int       `json:"downloaded"`
	Skipped    int       `json:"skipped"`
	Failed     int       `json:"failed"`
	Error      string    `json:"error,omitempty"`
}

type PathMapping struct {
	ID             int64  `json:"id"`
	DownloadPrefix string `json:"download_prefix"`
	LocalPrefix    string `json:"local_prefix"`
}

type PipelineStep struct {
	ID       int64           `json:"id"`
	Position int             `json:"position"`
	Trigger  string          `json:"trigger"`
	Type     string          `json:"type"`
	Name     string          `json:"name"`
	Config   json.RawMessage `json:"config"`
	Enabled  bool            `json:"enabled"`
	// TimeoutSeconds bounds how long this specific step's shell command or
	// StashApp GraphQL call may run before it is treated as failed. 0 means
	// "use the settings-wide default step timeout" (see
	// download.Service.pipelineTimeout) - each step can override that
	// default individually so a script known to take longer than the rest
	// of the pipeline doesn't need every other step's timeout raised too.
	TimeoutSeconds int `json:"timeout_seconds"`
}
type PipelineRun struct {
	DownloadID int64     `json:"download_id"`
	Trigger    string    `json:"trigger"`
	State      string    `json:"state"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}
type PipelineLog struct {
	ID            int64           `json:"id"`
	DownloadID    int64           `json:"download_id"`
	StepID        int64           `json:"step_id"`
	State         string          `json:"state"`
	Configuration json.RawMessage `json:"configuration"`
	Output        string          `json:"output"`
	Error         string          `json:"error,omitempty"`
	StartedAt     time.Time       `json:"started_at"`
	FinishedAt    time.Time       `json:"finished_at,omitempty"`
}

type Notification struct {
	ID        int64     `json:"id"`
	ReleaseID int64     `json:"release_id"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
	Release   *Release  `json:"release,omitempty"`
}

// ScheduleForecast is one background scheduled job's live enabled/interval
// state plus its next few predicted run times. Group loosely names which
// settings-panel category the schedule belongs to (e.g. "Download
// monitoring", "Scheduled scrapes", "StashApp sync") purely for display
// grouping - it carries no other meaning. NextRuns is empty when the
// schedule is disabled, has no valid interval/calendar configuration yet,
// or (calendar mode only) no match was found within the forecast horizon.
type ScheduleForecast struct {
	Group               string      `json:"group"`
	Name                string      `json:"name"`
	Enabled             bool        `json:"enabled"`
	Interval            string      `json:"interval,omitempty"`
	NextRuns            []time.Time `json:"next_runs,omitempty"`
	SiteGroupScheduleID int64       `json:"site_group_schedule_id,omitempty"`
}

// SiteGroupScheduleSite is one monitoring site's configured scrape mode
// within a SiteGroupSchedule.
type SiteGroupScheduleSite struct {
	SiteID int64  `json:"site_id"`
	Mode   string `json:"mode"` // "quick", "full", or "new"
}

// SiteGroupSchedule is one user-defined, independently named scrape
// schedule that runs a chosen scrape mode against a chosen subset of
// monitoring sites, each site free to use its own mode. Unlike the three
// built-in Quick/Full/New schedules (which always scan every enabled
// site), any number of these can be configured - see
// internal/monitor.expandSiteGroupSchedules for how each one is expanded
// into one synthetic per-site schedule and merged into the normal
// scheduling loop. ScheduleMode/Interval/StartTime/Weekdays/Cron mirror
// the timing fields the three built-in schedules already expose in
// Settings (basic/advanced/cron - see monitor.normalizeScheduleMode).
// Stored as a JSON array in the "site_group_schedules" setting (see
// ParseSiteGroupSchedules), following the same JSON-blob-in-one-setting
// pattern as byparr_instances (internal/scraper.ParseInstances).
type SiteGroupSchedule struct {
	ID           int64                   `json:"id"`
	Name         string                  `json:"name"`
	Enabled      bool                    `json:"enabled"`
	Priority     int                     `json:"priority"`
	ScheduleMode string                  `json:"schedule_mode"`
	Interval     string                  `json:"interval"`
	StartTime    string                  `json:"start_time"`
	Weekdays     string                  `json:"weekdays"`
	Cron         string                  `json:"cron"`
	Pages        int                     `json:"pages"`
	Sites        []SiteGroupScheduleSite `json:"sites"`
}

// ParseSiteGroupSchedules decodes the JSON array stored in the
// site_group_schedules setting. Tolerant of empty/invalid input (returns
// nil), matching scraper.ParseInstances' precedent for JSON-encoded list
// settings - validation of well-formedness belongs at the point the
// setting is saved (PUT /api/settings), not here.
func ParseSiteGroupSchedules(raw string) []SiteGroupSchedule {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var groups []SiteGroupSchedule
	if e := json.Unmarshal([]byte(raw), &groups); e != nil {
		return nil
	}
	return groups
}
