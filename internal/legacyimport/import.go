package legacyimport

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	stdhtml "html"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Net005/JAVBeacon/internal/store"
)

type Options struct {
	DatabasePath      string
	ResultsPath       string
	DetailsPath       string
	Provider          string
	ExistingSitesOnly bool
	Apply             bool
}

type Report struct {
	Applied              bool      `json:"applied"`
	StartedAt            time.Time `json:"started_at"`
	FinishedAt           time.Time `json:"finished_at"`
	DetailRows           int       `json:"detail_rows"`
	DuplicateDetails     int       `json:"duplicate_details"`
	ResultRows           int       `json:"result_rows"`
	RepairedRows         int       `json:"repaired_rows"`
	ValidRows            int       `json:"valid_rows"`
	Inserted             int       `json:"inserted"`
	Updated              int       `json:"updated"`
	MissingDetails       int       `json:"missing_details"`
	SkippedInvalidID     int       `json:"skipped_invalid_id"`
	SkippedTitleMismatch int       `json:"skipped_title_mismatch"`
	SkippedMalformed     int       `json:"skipped_malformed"`
	SitesCreated         int       `json:"sites_created"`
	ExistingSites        int       `json:"existing_sites"`
	JavLibraryRows       int       `json:"javlibrary_rows"`
	GIGARows             int       `json:"giga_rows"`
	SkippedProvider      int       `json:"skipped_provider"`
	SkippedUnknownSite   int       `json:"skipped_unknown_site"`
	FuzzyAddedDateRule   string    `json:"fuzzy_added_date_rule"`
	Examples             []string  `json:"examples,omitempty"`
}

type detail struct {
	ID, Title, Image, ReleaseDate, Length, Director, Maker, Label string
	Cast, Genres, ScraperID, Series, Source                       string
}

type result struct {
	ID, Image, Title, ScraperID, Source, SiteTitle string
	Added, Updated, ReleaseDate                    string
	Released, Local, Notified                      bool
}

type classifier struct {
	actresses, tags, makers, labels, series, directors map[string]bool
}

var catalogPartsPattern = regexp.MustCompile(`(?i)^(.*[^0-9])([0-9]+)([A-Z]*)$`)

func Run(ctx context.Context, options Options) (Report, error) {
	report := Report{Applied: options.Apply, StartedAt: time.Now().UTC(), FuzzyAddedDateRule: "deterministic 0-28 days before release date, with a deterministic time of day"}
	details, classes, repaired, duplicates, err := loadDetails(options.DetailsPath)
	report.DetailRows, report.RepairedRows, report.DuplicateDetails = len(details)+duplicates, repaired, duplicates
	if err != nil {
		return report, err
	}
	migrated, err := store.OpenSQLite(options.DatabasePath)
	if err != nil {
		return report, err
	}
	if err = migrated.Close(); err != nil {
		return report, err
	}
	db, err := sql.Open("sqlite", options.DatabasePath+"?_pragma=busy_timeout(30000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)")
	if err != nil {
		return report, err
	}
	defer db.Close()
	existing, err := existingKeys(ctx, db)
	if err != nil {
		return report, err
	}
	knownSites, err := existingSites(ctx, db)
	if err != nil {
		return report, err
	}
	report.ExistingSites = len(knownSites)

	var tx *sql.Tx
	if options.Apply {
		tx, err = db.BeginTx(ctx, nil)
		if err != nil {
			return report, err
		}
		defer tx.Rollback()
	}
	err = scanLines(options.ResultsPath, func(lineNumber int, fields []string) error {
		if lineNumber == 1 {
			return nil
		}
		report.ResultRows++
		r, fixed, parseErr := parseResult(fields)
		if parseErr != nil {
			report.SkippedMalformed++
			report.example(fmt.Sprintf("result line %d malformed: %v", lineNumber, parseErr))
			return nil
		}
		if fixed {
			report.RepairedRows++
		}
		if options.Provider != "" && !strings.EqualFold(r.Source, options.Provider) {
			report.SkippedProvider++
			return nil
		}
		siteKey := normalized(r.SiteTitle)
		if _, ok := knownSites[siteKey]; options.ExistingSitesOnly && !ok {
			report.SkippedUnknownSite++
			return nil
		}
		displayID, key := "", ""
		var info detail
		if strings.EqualFold(r.Source, "JavLibrary") {
			report.JavLibraryRows++
			var valid bool
			displayID, key, valid = validatedID(r.ID, r.Title)
			if !valid {
				report.SkippedInvalidID++
				report.example(fmt.Sprintf("result line %d invalid JavLibrary ID/title: %q / %q", lineNumber, r.ID, r.Title))
				return nil
			}
			var found bool
			info, found = details[strings.ToLower(strings.TrimSpace(r.ScraperID))]
			if !found {
				report.MissingDetails++
				report.example(fmt.Sprintf("result line %d has no details for scraper ID %s", lineNumber, r.ScraperID))
			} else {
				detailID, detailKey, detailValid := validatedID(info.ID, info.Title)
				if !detailValid || detailKey != key {
					report.SkippedTitleMismatch++
					report.example(fmt.Sprintf("result line %d detail mismatch: %s vs %s (%s)", lineNumber, displayID, detailID, r.ScraperID))
					return nil
				}
				displayID = detailID
			}
		} else if strings.EqualFold(r.Source, "GIGA") {
			report.GIGARows++
			if _, valid := catalogKey(r.ID); !valid {
				report.SkippedInvalidID++
				report.example(fmt.Sprintf("result line %d invalid GIGA ID: %q", lineNumber, r.ID))
				return nil
			}
			displayID = gigaDisplayID(r.ID)
		} else {
			report.SkippedMalformed++
			return nil
		}
		report.ValidRows++
		if _, ok := knownSites[siteKey]; !ok {
			knownSites[siteKey] = 0
			report.SitesCreated++
			if options.Apply {
				siteType := classes.siteType(r.SiteTitle)
				if _, err := tx.ExecContext(ctx, `INSERT INTO sites(title,type,name,url,notify,download,watchlist,rss_url,enabled,created_at,updated_at) VALUES(?,?,?,?,0,0,0,'',0,?,?) ON CONFLICT(title) DO NOTHING`, r.SiteTitle, siteType, r.Source, "", time.Now().UTC(), time.Now().UTC()); err != nil {
					return err
				}
				var siteID int64
				if err := tx.QueryRowContext(ctx, `SELECT id FROM sites WHERE title=?`, r.SiteTitle).Scan(&siteID); err != nil {
					return err
				}
				knownSites[siteKey] = siteID
			}
		}
		keyWithSite := r.SiteTitle + "\x00" + displayID
		if existing[keyWithSite] {
			report.Updated++
		} else {
			report.Inserted++
			existing[keyWithSite] = true
		}
		if !options.Apply {
			return nil
		}
		return upsert(ctx, tx, knownSites[siteKey], displayID, r, info)
	})
	if err != nil {
		return report, err
	}
	if options.Apply {
		if err = tx.Commit(); err != nil {
			return report, err
		}
	}
	report.FinishedAt = time.Now().UTC()
	return report, nil
}

func (r *Report) example(value string) {
	if len(r.Examples) < 20 {
		r.Examples = append(r.Examples, value)
	}
}

func loadDetails(path string) (map[string]detail, classifier, int, int, error) {
	out := map[string]detail{}
	c := classifier{map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}}
	repaired, duplicates := 0, 0
	err := scanLines(path, func(lineNumber int, fields []string) error {
		if lineNumber == 1 {
			return nil
		}
		d, fixed, err := parseDetail(fields)
		if err != nil {
			return fmt.Errorf("details line %d: %w", lineNumber, err)
		}
		if fixed {
			repaired++
		}
		key := strings.ToLower(strings.TrimSpace(d.ScraperID))
		if key == "" {
			return fmt.Errorf("details line %d: empty scraper ID", lineNumber)
		}
		if _, exists := out[key]; exists {
			duplicates++
			return nil
		}
		out[key] = d
		c.add(d)
		return nil
	})
	return out, c, repaired, duplicates, err
}

func scanLines(path string, fn func(int, []string) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		raw := strings.TrimSuffix(scanner.Text(), "\r")
		if err := fn(line, strings.Split(raw, "\t")); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func parseResult(fields []string) (result, bool, error) {
	var r result
	source := -1
	for i := 3; i < len(fields); i++ {
		if fields[i] == "GIGA" || fields[i] == "JavLibrary" {
			source = i
			break
		}
	}
	if source < 4 || len(fields)-source != 10 {
		return r, false, fmt.Errorf("expected source plus 9 trailing fields, got %d columns", len(fields))
	}
	r.ID, r.Image = strings.TrimSpace(fields[0]), strings.TrimSpace(fields[1])
	r.Title = joinedTitle(fields[2 : source-1])
	r.ScraperID, r.Source, r.SiteTitle = strings.TrimSpace(fields[source-1]), fields[source], strings.TrimSpace(fields[source+1])
	r.Added, r.Updated, r.ReleaseDate = fields[source+2], fields[source+3], strings.TrimSpace(fields[source+5])
	r.Released, r.Local, r.Notified = parseBool(fields[source+6]), parseBool(fields[source+7]), parseBool(fields[source+8])
	if r.ID == "" || r.Title == "" || r.SiteTitle == "" {
		return r, false, errors.New("required value is empty")
	}
	return r, len(fields) != 14, nil
}

func parseDetail(fields []string) (detail, bool, error) {
	var d detail
	image := -1
	for i := 3; i < len(fields); i++ {
		if _, err := time.Parse("2006-01-02", strings.TrimSpace(fields[i])); err == nil {
			image = i - 1
			break
		}
	}
	if image < 2 || len(fields)-image != 14 {
		return d, false, fmt.Errorf("expected image plus 13 trailing fields, got %d columns", len(fields))
	}
	d.ID = strings.TrimSpace(fields[0])
	d.Title = joinedTitle(fields[1:image])
	d.Image, d.ReleaseDate, d.Length, d.Director = fields[image], fields[image+1], fields[image+2], fields[image+3]
	d.Maker, d.Label, d.Cast, d.Genres = fields[image+4], fields[image+5], fields[image+6], fields[image+7]
	d.ScraperID, d.Series, d.Source = fields[image+8], fields[image+12], strings.TrimSpace(fields[image+13])
	if d.ID == "" || d.Title == "" || d.Source != "JavLibrary" {
		return d, false, errors.New("required detail value is empty or source is invalid")
	}
	return d, len(fields) != 16, nil
}

func validatedID(raw, title string) (string, string, bool) {
	words := strings.Fields(strings.TrimSpace(title))
	if len(words) == 0 {
		return "", "", false
	}
	titleID := strings.ToUpper(strings.Trim(words[0], "[](){}:;,."))
	rawPrefix, rawNumber, rawSuffix, rawOK := catalogParts(raw)
	titlePrefix, titleNumber, titleSuffix, titleOK := catalogParts(titleID)
	prefixMatches := rawPrefix == titlePrefix || rawPrefix == titlePrefix+titleSuffix || titlePrefix == rawPrefix+rawSuffix || strings.HasSuffix(titlePrefix, rawPrefix) || strings.HasSuffix(rawPrefix, titlePrefix)
	if !rawOK || !titleOK || rawNumber != titleNumber || !prefixMatches {
		return titleID, titlePrefix + titleNumber + titleSuffix, false
	}
	return titleID, titlePrefix + titleNumber + titleSuffix, true
}

func catalogKey(value string) (string, bool) {
	prefix, number, suffix, ok := catalogParts(value)
	return prefix + number + suffix, ok
}

func catalogParts(value string) (string, string, string, bool) {
	compact := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' {
			return r - 32
		}
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return -1
	}, value)
	match := catalogPartsPattern.FindStringSubmatch(compact)
	if len(match) != 4 {
		return "", "", "", false
	}
	number := strings.TrimLeft(match[2], "0")
	if number == "" {
		number = "0"
	}
	return match[1], number, match[3], true
}

func gigaDisplayID(raw string) string {
	prefix, number, suffix, ok := catalogParts(raw)
	if !ok {
		return strings.ToUpper(strings.TrimSpace(raw))
	}
	return prefix + "-" + number + suffix
}

func (c classifier) add(d detail) {
	addValues(c.actresses, d.Cast)
	addValues(c.tags, d.Genres)
	addValue(c.makers, d.Maker)
	addValue(c.labels, d.Label)
	addValue(c.series, d.Series)
	addValue(c.directors, d.Director)
}

func addValues(target map[string]bool, values string) {
	for _, value := range strings.Split(values, "|") {
		addValue(target, value)
	}
}

func addValue(target map[string]bool, value string) {
	if value = normalized(value); value != "" {
		target[value] = true
	}
}

func normalized(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func (c classifier) siteType(title string) string {
	key := normalized(title)
	switch {
	case title == "GIGA" || title == "JavLibraryRecent":
		return "Site"
	case c.actresses[key]:
		return "Actress"
	case c.tags[key]:
		return "Tag"
	case c.makers[key]:
		return "Maker"
	case c.labels[key]:
		return "Label"
	case c.series[key]:
		return "Series"
	case c.directors[key]:
		return "Director"
	default:
		return "Site"
	}
}

func existingSites(ctx context.Context, db *sql.DB) (map[string]int64, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,title FROM sites`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id int64
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			return nil, err
		}
		out[normalized(title)] = id
	}
	return out, rows.Err()
}

func existingKeys(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT s.title,r.video_id FROM releases r JOIN sites s ON s.id=r.site_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var site, id string
		if err := rows.Scan(&site, &id); err != nil {
			return nil, err
		}
		out[site+"\x00"+id] = true
	}
	return out, rows.Err()
}

func upsert(ctx context.Context, tx *sql.Tx, siteID int64, videoID string, r result, d detail) error {
	title, image, releaseDate := cleanLegacyText(r.Title), cleanLegacyText(r.Image), cleanLegacyText(r.ReleaseDate)
	director, studio, actress, duration := "", "", "", ""
	genres := []string{}
	if d.Title != "" {
		title, image, releaseDate = cleanLegacyText(d.Title), cleanLegacyText(prefer(d.Image, image)), cleanLegacyText(prefer(d.ReleaseDate, releaseDate))
		director, studio, actress = cleanLegacyText(d.Director), cleanLegacyText(prefer(d.Maker, d.Label)), strings.Join(splitLegacyActresses(d.Cast), ", ")
		genres = splitLegacyValues(d.Genres)
		if strings.TrimSpace(d.Length) != "" {
			duration = cleanLegacyText(d.Length) + " min"
		}
	}
	genresJSON, _ := json.Marshal(genres)
	added := fuzzyAddedAt(releaseDate, r.SiteTitle+"\x00"+videoID)
	if added.IsZero() {
		added = parseLegacyTime(r.Added)
	}
	if added.IsZero() {
		added = time.Now().UTC()
	}
	updated := parseLegacyTime(r.Updated)
	if updated.IsZero() {
		updated = added
	}
	productURL := ""
	if strings.EqualFold(r.Source, "JavLibrary") && r.ScraperID != "" {
		productURL = "https://www.javlibrary.com/en/?v=" + url.QueryEscape(r.ScraperID)
	} else if strings.EqualFold(r.Source, "GIGA") && r.ScraperID != "" {
		productURL = "https://www.akiba-web.com/product/product.php?product_id=" + url.QueryEscape(r.ScraperID)
	}
	identityKey := legacyReleaseIdentity(r.Source, videoID)
	_, err := tx.ExecContext(ctx, `INSERT INTO releases(site_id,identity_key,video_id,scraper_id,title,release_date,source,image_url,product_url,actress,director,studio,genres,duration,story,screenshots,released,is_local,notified,notify_on_release,watchlist,monitor_download,stash_scene_id,added_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,'','[]',?,?,?,0,0,0,'',?,?)
		ON CONFLICT DO UPDATE SET
		identity_key=COALESCE(NULLIF(releases.identity_key,''),excluded.identity_key),scraper_id=COALESCE(NULLIF(excluded.scraper_id,''),releases.scraper_id),title=COALESCE(NULLIF(excluded.title,''),releases.title),release_date=COALESCE(NULLIF(excluded.release_date,''),releases.release_date),source=COALESCE(NULLIF(excluded.source,''),releases.source),image_url=COALESCE(NULLIF(excluded.image_url,''),releases.image_url),product_url=COALESCE(NULLIF(excluded.product_url,''),releases.product_url),actress=COALESCE(NULLIF(excluded.actress,''),releases.actress),director=COALESCE(NULLIF(excluded.director,''),releases.director),studio=COALESCE(NULLIF(excluded.studio,''),releases.studio),genres=CASE WHEN excluded.genres='[]' THEN releases.genres ELSE excluded.genres END,duration=COALESCE(NULLIF(excluded.duration,''),releases.duration),released=MAX(releases.released,excluded.released),is_local=MAX(releases.is_local,excluded.is_local),notified=MAX(releases.notified,excluded.notified),added_at=MIN(releases.added_at,excluded.added_at),updated_at=MAX(releases.updated_at,excluded.updated_at)`,
		siteID, identityKey, videoID, cleanLegacyText(r.ScraperID), title, releaseDate, cleanLegacyText(r.Source), image, productURL, actress, director, studio, string(genresJSON), duration, r.Released, r.Local, r.Notified, added, updated)
	if err != nil {
		return err
	}
	var releaseID int64
	var effectiveActress, effectiveGenres string
	if err := tx.QueryRowContext(ctx, `SELECT id,actress,genres FROM releases WHERE identity_key=?`, identityKey).Scan(&releaseID, &effectiveActress, &effectiveGenres); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO release_sites(release_id,site_id,site_monitor_download) SELECT ?,id,0 FROM sites WHERE id=? ON CONFLICT(release_id,site_id) DO NOTHING`, releaseID, siteID); err != nil {
		return err
	}
	var effectiveTags []string
	_ = json.Unmarshal([]byte(effectiveGenres), &effectiveTags)
	return store.SyncReleaseMetadata(ctx, tx, releaseID, effectiveActress, effectiveTags)
}

func prefer(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func joinedTitle(parts []string) string {
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}

func splitValues(raw string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range strings.Split(raw, "|") {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

var legacyHTMLTag = regexp.MustCompile(`(?s)<[^>]*>`)
var legacyNamedHTMLEntity = regexp.MustCompile(`(?i)&(?:amp|lt|gt|quot|apos);`)
var legacyParenthesizedActress = regexp.MustCompile(`\(([^()]*)\)`)

func cleanLegacyText(value string) string {
	for range 2 {
		value = legacyNamedHTMLEntity.ReplaceAllStringFunc(value, strings.ToLower)
		decoded := stdhtml.UnescapeString(value)
		if decoded == value {
			break
		}
		value = decoded
	}
	value = legacyHTMLTag.ReplaceAllString(value, " ")
	return strings.Join(strings.Fields(value), " ")
}

func splitLegacyValues(raw string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range strings.Split(raw, "|") {
		value = cleanLegacyText(value)
		key := strings.ToLower(value)
		if value != "" && !seen[key] {
			seen[key] = true
			out = append(out, value)
		}
	}
	return out
}

func splitLegacyActresses(raw string) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(value string) {
		value = cleanLegacyText(strings.Trim(value, "() "))
		key := strings.ToLower(value)
		if value != "" && !seen[key] {
			seen[key] = true
			out = append(out, value)
		}
	}
	for _, value := range strings.Split(raw, "|") {
		matches := legacyParenthesizedActress.FindAllStringSubmatch(value, -1)
		add(legacyParenthesizedActress.ReplaceAllString(value, " "))
		for _, match := range matches {
			add(match[1])
		}
	}
	return out
}

func legacyReleaseIdentity(source, videoID string) string {
	key := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' {
			return r - 32
		}
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return -1
	}, strings.TrimSpace(videoID))
	if key == "" {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(source)) + ":" + key
}

func parseBool(raw string) bool {
	v, _ := strconv.ParseBool(strings.TrimSpace(raw))
	if strings.TrimSpace(raw) == "1" {
		return true
	}
	return v
}

func parseLegacyTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	for _, layout := range []string{"2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05Z", time.RFC3339Nano} {
		if parsed, err := time.Parse(layout, raw); err == nil && parsed.Year() > 1970 {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func fuzzyAddedAt(releaseDate, seed string) time.Time {
	date, err := time.Parse("2006-01-02", strings.TrimSpace(releaseDate))
	if err != nil {
		return time.Time{}
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(seed))
	value := h.Sum64()
	days := time.Duration(value%29) * 24 * time.Hour
	seconds := time.Duration((value/29)%86400) * time.Second
	return date.UTC().Add(-days).Add(seconds)
}
