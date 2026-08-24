package download

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQBittorrentVersionTestsLoginAndAPI(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("username") != "user" || r.FormValue("password") != "secret" {
			http.Error(w, "Fails.", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte("Ok."))
	})
	mux.HandleFunc("GET /api/v2/app/version", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("v5.1.2"))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	version, err := NewQB(server.URL, "user", "secret").Version(context.Background())
	if err != nil || version != "v5.1.2" {
		t.Fatalf("version=%q err=%v", version, err)
	}
	if _, err := NewQB(server.URL, "user", "wrong").Version(context.Background()); err == nil {
		t.Fatal("expected invalid qBittorrent credentials to fail")
	}
}

func TestQBittorrentVersionAcceptsEmptyNoContentLogin(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v2/auth/login", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/v2/app/version", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("v5.1.2"))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	version, err := NewQB(server.URL, "user", "secret").Version(context.Background())
	if err != nil || version != "v5.1.2" {
		t.Fatalf("version=%q err=%v", version, err)
	}
}

func TestQBittorrentAddResolvesCategoryCaseInsensitively(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v2/auth/login", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("Ok."))
	})
	mux.HandleFunc("GET /api/v2/torrents/categories", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"JAV":{"name":"JAV","savePath":"/downloads/jav"},"Other":{"name":"Other","savePath":""}}`))
	})
	mux.HandleFunc("POST /api/v2/torrents/add", func(w http.ResponseWriter, r *http.Request) {
		if got := r.FormValue("category"); got != "JAV" {
			t.Fatalf("category=%q, want canonical qBittorrent category JAV", got)
		}
		if !strings.HasPrefix(r.FormValue("urls"), "magnet:") {
			t.Fatalf("missing magnet URL: %q", r.FormValue("urls"))
		}
		_, _ = w.Write([]byte("Ok."))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	if _, err := NewQB(server.URL, "user", "secret").Add(context.Background(), "magnet:?xt=urn:btih:test", "jav"); err != nil {
		t.Fatal(err)
	}
	if _, err := NewQB(server.URL, "user", "secret").Add(context.Background(), "magnet:?xt=urn:btih:test", "missing"); err == nil {
		t.Fatal("expected an unknown category to be rejected")
	}
}

func TestQBittorrentTorrentsParsesSeenComplete(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v2/auth/login", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("Ok.")) })
	mux.HandleFunc("GET /api/v2/torrents/info", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"hash":"abc","name":"TEST-1","num_seeds":0,"num_leechs":4,"seen_complete":1724515200}]`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	rows, err := NewQB(server.URL, "user", "secret").Torrents(context.Background())
	if err != nil || len(rows) != 1 || rows[0].SeenComplete != 1724515200 || rows[0].Peers != 4 {
		t.Fatalf("torrents=%+v err=%v", rows, err)
	}
}
