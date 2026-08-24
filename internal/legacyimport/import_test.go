package legacyimport

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
)

func TestValidatedIDUsesCapitalizedTitlePrefix(t *testing.T) {
	for _, test := range []struct {
		raw, title, want string
	}{
		{"same097", "SAME-097 A title", "SAME-097"},
		{"KLD002", "KLD-02 A title", "KLD-02"},
		{"SDJSv242", "SDJS-242v A title", "SDJS-242V"},
		{"QBD050", "BDQBD-050 A title", "BDQBD-050"},
	} {
		got, _, ok := validatedID(test.raw, test.title)
		if !ok || got != test.want {
			t.Fatalf("validatedID(%q, %q) = %q, %v; want %q", test.raw, test.title, got, ok, test.want)
		}
	}
	if _, _, ok := validatedID("YNO006", "YNO-064 Wrong title"); ok {
		t.Fatal("mismatched numeric ID was accepted")
	}
}

func TestImportRepairsEmbeddedTabsAndAppliesFuzzyDates(t *testing.T) {
	dir := t.TempDir()
	resultsPath, detailsPath := filepath.Join(dir, "results.txt"), filepath.Join(dir, "details.txt")
	results := "ID\tImage\tTitle\tScraperVideoID\tSource\tMonitorSiteTitle\tResultAdded\tResultUpdated\tSortOrder\tReleaseDate\tIsReleased\tIsLocal\tIsNotified\tNotificationDate\r\n" +
		"WANZ744\thttps://example.test/wanz.jpg\tWANZ-744 Sample - \tJulia\tjav-test\tJavLibrary\tActress One\t2024-01-01 00:00:00.000\t2024-02-01 00:00:00.000\t1\t2024-03-05\t1\t0\t1\t\r\n" +
		"TOR06\thttps://example.test/tor.jpg\tCyber Agent\t1448\tGIGA\tGIGA\t2022-01-01 00:00:00.000\t2022-02-01 00:00:00.000\t2\t2001-11-10\t1\t1\t0\t\r\n" +
		"YNO006\thttps://example.test/bad.jpg\tYNO-064 Wrong\tjav-bad\tJavLibrary\tBad Site\t2024-01-01 00:00:00.000\t2024-02-01 00:00:00.000\t3\t2024-03-05\t1\t0\t0\t\r\n"
	details := "ID\tTitle\tImage\tReleaseDate\tLength\tDirector\tMaker\tLabel\tCastMember\tGenres\tScraperVideoID\tRating\tDateAdded\tDateUpdated\tSeries\tSource\r\n" +
		"wanz744\tWANZ-744 Sample - \tJulia\thttps://example.test/wanz-detail.jpg\t2024-03-05\t120\tDirector\tMaker\tLabel\tActress One | Actress Two\tDrama | Cosplay\tjav-test\t0\t2024-02-01 00:00:00Z\t1970-01-01 00:00:00Z\tSeries\tJavLibrary\r\n"
	if err := os.WriteFile(resultsPath, []byte(results), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(detailsPath, []byte(details), 0644); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(dir, "javbeacon.db")
	report, err := Run(context.Background(), Options{DatabasePath: database, ResultsPath: resultsPath, DetailsPath: detailsPath, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.ValidRows != 2 || report.Inserted != 2 || report.SkippedInvalidID != 1 || report.RepairedRows != 2 {
		t.Fatalf("unexpected report: %+v", report)
	}
	st, err := store.OpenSQLite(database)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rows, err := st.Releases(context.Background(), domain.ReleaseFilter{Search: "WANZ-744", Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	got := rows[0]
	if got.VideoID != "WANZ-744" || got.Title != "WANZ-744 Sample - Julia" || got.Actress != "Actress One, Actress Two" || got.Studio != "Maker" || len(got.Genres) != 2 {
		t.Fatalf("unexpected imported release: %+v", got)
	}
	releaseDate, _ := time.Parse("2006-01-02", got.ReleaseDate)
	if got.AddedAt.Before(releaseDate.Add(-28*24*time.Hour)) || !got.AddedAt.Before(releaseDate.Add(24*time.Hour)) {
		t.Fatalf("added_at %v is outside fuzzy release-date window", got.AddedAt)
	}
	giga, err := st.Releases(context.Background(), domain.ReleaseFilter{Search: "TOR-6", Limit: 10})
	if err != nil || len(giga) != 1 || giga[0].VideoID != "TOR-6" {
		t.Fatalf("GIGA normalization failed: %+v err=%v", giga, err)
	}
}

func TestImportCanRestrictToExistingJavLibrarySitesAndNormalizeMetadata(t *testing.T) {
	dir := t.TempDir()
	database := filepath.Join(dir, "javbeacon.db")
	st, err := store.OpenSQLite(database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveSite(context.Background(), domain.Site{Title: "Neo Akari", Type: "Actress", Name: "JavLibrary", URL: "https://example.test/neo", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	resultsPath, detailsPath := filepath.Join(dir, "results.txt"), filepath.Join(dir, "details.txt")
	results := "ID\tImage\tTitle\tScraperVideoID\tSource\tMonitorSiteTitle\tResultAdded\tResultUpdated\tSortOrder\tReleaseDate\tIsReleased\tIsLocal\tIsNotified\tNotificationDate\n" +
		"mizd181\thttps://example.test/mizd.jpg\tMIZD-181 &amp; Sample\tjav-mizd\tJavLibrary\tNeo Akari\t2024-01-01 00:00:00.000\t2024-02-01 00:00:00.000\t1\t2024-03-05\t1\t0\t0\t\n" +
		"ABC001\thttps://example.test/abc.jpg\tABC-001 Removed site\tjav-abc\tJavLibrary\tRemoved Site\t2024-01-01 00:00:00.000\t2024-02-01 00:00:00.000\t2\t2024-03-05\t1\t0\t0\t\n" +
		"TOR06\thttps://example.test/tor.jpg\tCyber Agent\t1448\tGIGA\tGIGA\t2022-01-01 00:00:00.000\t2022-02-01 00:00:00.000\t3\t2001-11-10\t1\t0\t0\t\n"
	details := "ID\tTitle\tImage\tReleaseDate\tLength\tDirector\tMaker\tLabel\tCastMember\tGenres\tScraperVideoID\tRating\tDateAdded\tDateUpdated\tSeries\tSource\n" +
		"mizd181\tMIZD-181 &Amp; &lt;b&gt;Sample&lt;/b&gt;\thttps://example.test/mizd-detail.jpg\t2024-03-05\t120\tDirector\tM&#039;s Video Group\tLabel\tNeo Akari | Actress Two (Actress Three)\tBest, Omnibus | Cosplay\tjav-mizd\t0\t2024-02-01 00:00:00Z\t1970-01-01 00:00:00Z\tSeries\tJavLibrary\n" +
		"abc001\tABC-001 Removed site\thttps://example.test/abc-detail.jpg\t2024-03-05\t90\tDirector\tMaker\tLabel\tOther Actress\tDrama\tjav-abc\t0\t2024-02-01 00:00:00Z\t1970-01-01 00:00:00Z\tSeries\tJavLibrary\n"
	if err := os.WriteFile(resultsPath, []byte(results), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(detailsPath, []byte(details), 0644); err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), Options{DatabasePath: database, ResultsPath: resultsPath, DetailsPath: detailsPath, Provider: "JavLibrary", ExistingSitesOnly: true, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.ValidRows != 1 || report.Inserted != 1 || report.SkippedUnknownSite != 1 || report.SkippedProvider != 1 || report.SitesCreated != 0 {
		t.Fatalf("unexpected filtered report: %+v", report)
	}
	st, err = store.OpenSQLite(database)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rows, err := st.Releases(context.Background(), domain.ReleaseFilter{Search: "MIZD-181", Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	got := rows[0]
	if got.VideoID != "MIZD-181" || got.Title != "MIZD-181 & Sample" || got.Studio != "M's Video Group" {
		t.Fatalf("cleaned release=%+v", got)
	}
	if strings.Join(got.Actresses, "|") != "Neo Akari|Actress Two|Actress Three" {
		t.Fatalf("actresses=%v", got.Actresses)
	}
	if strings.Join(got.Genres, "|") != "Best, Omnibus|Cosplay" {
		t.Fatalf("tags=%v", got.Genres)
	}
	sites, err := st.Sites(context.Background())
	if err != nil || len(sites) != 1 {
		t.Fatalf("sites=%+v err=%v", sites, err)
	}
}
