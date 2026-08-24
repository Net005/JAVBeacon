package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/download"
	"github.com/Net005/JAVBeacon/internal/store"
)

// qbAddEchoStub is a minimal qBittorrent stand-in that behaves like the real
// thing for the one property Service.Download's post-Add verification
// depends on: a torrent only shows up in /torrents/info once it was
// genuinely added, named from the magnet's own dn= parameter (which is how
// Sukebei/Nyaa magnets carry the release title, hash included). Set reject
// to simulate qBittorrent replying "Ok." to /add without ever actually
// queuing anything - the exact silent-failure case Download's verification
// step exists to catch.
func qbAddEchoStub(reject bool) *httptest.Server {
	var mu sync.Mutex
	var added []string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v2/auth/login", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("Ok."))
	})
	mux.HandleFunc("POST /api/v2/torrents/add", func(w http.ResponseWriter, r *http.Request) {
		if !reject {
			mu.Lock()
			added = append(added, r.FormValue("urls"))
			mu.Unlock()
		}
		_, _ = w.Write([]byte("Ok."))
	})
	mux.HandleFunc("GET /api/v2/torrents/info", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		links := append([]string(nil), added...)
		mu.Unlock()
		type torrent struct {
			Hash string `json:"hash"`
			Name string `json:"name"`
		}
		rows := make([]torrent, 0, len(links))
		for i, link := range links {
			name := ""
			if u, e := url.Parse(link); e == nil {
				name = u.Query().Get("dn")
			}
			rows = append(rows, torrent{Hash: fmt.Sprintf("%040d", i), Name: name})
		}
		_ = json.NewEncoder(w).Encode(rows)
	})
	return httptest.NewServer(mux)
}

// TestReprSearchAndDownloadQueuesForBothNormalAndForced reproduces the
// reported "Force Download / normal Download on Search & Download page does
// not get added to the queue" bug end-to-end: a real qBittorrent stub, a
// real download.Service, and the actual downloadRelease HTTP handler, fed
// exactly the JSON body shape the browser sends (result spread + forced).
// It covers both the two working paths and the actual root cause fixed
// here: qBittorrent can reply "Ok." to /add without ever queuing anything,
// and that must now surface as a failed download instead of a silent,
// permanently-stuck "downloading" record.
func TestReprSearchAndDownloadQueuesForBothNormalAndForced(t *testing.T) {
	newServer := func(t *testing.T, qbURL string) (*Server, func(id int64) domain.Release) {
		t.Helper()
		ctx := context.Background()
		st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "repro.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		if err := st.SaveSettings(ctx, map[string]string{
			"accepted_patterns": "trusted@",
			"qb_url":            qbURL,
			"qb_username":       "user",
			"qb_password":       "secret",
		}); err != nil {
			t.Fatal(err)
		}
		site, _ := st.SaveSite(ctx, domain.Site{Title: "Test", Type: "Site", Name: "JavLibrary", Enabled: true})
		var n int
		makeRelease := func(id int64) domain.Release {
			n++
			videoID := fmt.Sprintf("PRED-%d", 900+n)
			if _, err := st.UpsertRelease(ctx, domain.Release{SiteID: site.ID, VideoID: videoID, Title: "Test", Source: "JavLibrary", Released: true}); err != nil {
				t.Fatal(err)
			}
			releases, err := st.Releases(ctx, domain.ReleaseFilter{Search: videoID, Limit: 10})
			if err != nil || len(releases) != 1 {
				t.Fatalf("release setup failed for %s: rows=%+v err=%v", videoID, releases, err)
			}
			return releases[0]
		}
		downloads := download.New(st, time.Second, slog.Default())
		return &Server{store: st, downloads: downloads, log: slog.Default()}, makeRelease
	}

	post := func(t *testing.T, s *Server, id int64, body domain.SearchResult) (int, map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/releases/"+itoa(id)+"/download", bytes.NewReader(raw))
		req.SetPathValue("id", itoa(id))
		rec := httptest.NewRecorder()
		s.downloadRelease(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	t.Run("normal accepted download that qBittorrent genuinely queues", func(t *testing.T) {
		qb := qbAddEchoStub(false)
		defer qb.Close()
		s, makeRelease := newServer(t, qb.URL)
		rel := makeRelease(0)
		link := "magnet:?xt=urn:btih:" + fmt.Sprintf("%040x", 1) + "&dn=" + url.QueryEscape("trusted@"+rel.VideoID)
		code, out := post(t, s, rel.ID, domain.SearchResult{Provider: "Sukebei/Nyaa", Title: "trusted@" + rel.VideoID, Link: link, Accepted: true})
		if code != 202 {
			t.Fatalf("expected 202, got %d body=%+v", code, out)
		}
		if out["status"] != "downloading" {
			t.Fatalf("expected status=downloading once the torrent is genuinely queued, got %+v", out)
		}
	})

	t.Run("forced download of a rejected result that qBittorrent genuinely queues", func(t *testing.T) {
		qb := qbAddEchoStub(false)
		defer qb.Close()
		s, makeRelease := newServer(t, qb.URL)
		rel := makeRelease(0)
		link := "magnet:?xt=urn:btih:" + fmt.Sprintf("%040x", 2) + "&dn=" + url.QueryEscape("untrusted "+rel.VideoID)
		code, out := post(t, s, rel.ID, domain.SearchResult{Provider: "Sukebei/Nyaa", Title: "untrusted " + rel.VideoID, Link: link, Forced: true})
		if code != 202 {
			t.Fatalf("expected 202, got %d body=%+v", code, out)
		}
		if out["status"] != "downloading" {
			t.Fatalf("expected status=downloading once the torrent is genuinely queued, got %+v", out)
		}
	})

	t.Run("qBittorrent replies Ok to add but never actually queues the torrent - the reported bug", func(t *testing.T) {
		qb := qbAddEchoStub(true)
		defer qb.Close()
		s, makeRelease := newServer(t, qb.URL)
		rel := makeRelease(0)
		link := "magnet:?xt=urn:btih:" + fmt.Sprintf("%040x", 3) + "&dn=" + url.QueryEscape("trusted@"+rel.VideoID)
		code, out := post(t, s, rel.ID, domain.SearchResult{Provider: "Sukebei/Nyaa", Title: "trusted@" + rel.VideoID, Link: link, Accepted: true})
		if code != 202 {
			t.Fatalf("expected 202, got %d body=%+v", code, out)
		}
		if out["status"] != "failed" {
			t.Fatalf("qBittorrent silently dropping the add must surface as failed, not a stuck downloading record: %+v", out)
		}
		if out["error"] == nil || out["error"] == "" {
			t.Fatalf("expected an explanatory error for the silent drop, got %+v", out)
		}
	})
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}
