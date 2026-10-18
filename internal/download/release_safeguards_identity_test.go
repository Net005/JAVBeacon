package download

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/Net005/JAVBeacon/internal/domain"
)

func TestSafeguardZeroByteVideoIsARejectedPayload(t *testing.T) {
	s, _, _ := safeguardTestService(t)
	path := filepath.Join(t.TempDir(), "TEST-123.mp4")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	err := s.payloadSafeguards(context.Background(), []string{path})
	var rejected *releaseSafeguardError
	if !errors.As(err, &rejected) {
		t.Fatalf("empty video was not rejected by filesize safeguard: %v", err)
	}
}

func TestSafeguardRejectsSmallHTTPHeadersBeforeBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "20")
		w.WriteHeader(200)
		w.Write([]byte("synthetic-small-file"))
	}))
	defer server.Close()
	file, err := os.CreateTemp(t.TempDir(), "partial")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var transferred atomic.Int64
	err = downloadHTTPSingleStream(context.Background(), server.Client(), resolvedHTTPFile{URL: server.URL, MinimumSize: 200_000_000}, file, 0, &transferred)
	var rejected *releaseSafeguardError
	if !errors.As(err, &rejected) || transferred.Load() != 0 {
		t.Fatalf("small response body transferred: bytes=%d err=%v", transferred.Load(), err)
	}
}

func TestSafeguardNonPreferredMainVideoSizeCannotHideInBundle(t *testing.T) {
	s, _, r := safeguardTestService(t)
	result := domain.SearchResult{Link: "https://downloads.example.com/synthetic.torrent", Title: "TEST-123 bundle", Transport: "torrent", Accepted: true, SizeBytes: 1_000_000_000, FileDetails: []domain.SearchFile{{Name: "TEST-123.mp4", SizeBytes: 100_000_000}, {Name: "OTHER-999.mp4", SizeBytes: 900_000_000}}}
	d, err := s.Download(context.Background(), r, result, "Synthetic", result.Link)
	if err != nil || d.PostStatus != postStatusBlacklistedRelease {
		t.Fatalf("small non-preferred main video acquired: %+v %v", d, err)
	}
}

func TestSafeguardUnrelatedLargerVideoCannotMaskShortMatch(t *testing.T) {
	s, st, r := safeguardTestService(t)
	ctx := context.Background()
	dir := t.TempDir()
	probeDir := t.TempDir()
	script := "#!/bin/sh\nfor target; do :; done\ncase \"$target\" in *TEST-123.mp4) duration=599;; *) duration=7200;; esac\nprintf '{\"format\":{\"duration\":\"%s\"},\"streams\":[{\"codec_type\":\"video\",\"nb_read_packets\":\"100\"}]}' \"$duration\"\n"
	if err := os.WriteFile(filepath.Join(probeDir, "ffprobe"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", probeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for name, size := range map[string]int64{"TEST-123.mp4": 200_000_000, "OTHER-999.mp4": 300_000_000} {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
		if err = os.Truncate(filepath.Join(dir, name), size); err != nil {
			t.Fatal(err)
		}
	}
	d, err := st.SaveDownload(ctx, domain.Download{ReleaseID: r.ID, Query: r.VideoID, Name: r.VideoID, Transport: "torrent", Status: "downloading", TransferReference: "https://downloads.example.com/synthetic.torrent"})
	if err != nil {
		t.Fatal(err)
	}
	if !s.handleTorrentVideoCheck(ctx, nil, &d, Torrent{ContentPath: dir}) || d.PostStatus != postStatusBlacklistedRelease {
		t.Fatalf("unrelated video masked a short matching payload: %+v", d)
	}
}
