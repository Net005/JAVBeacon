package download

import (
	"context"
	"testing"
)

// fakeQBittorrentList is a minimal QBittorrent stand-in for testing
// verifyAddedToQBittorrent's matching logic in isolation, without spinning
// up an HTTP stub for every case.
type fakeQBittorrentList struct{ torrents []Torrent }

func (f fakeQBittorrentList) Torrents(context.Context) ([]Torrent, error) { return f.torrents, nil }
func (fakeQBittorrentList) Add(context.Context, string, string) (string, error) {
	return "", nil
}
func (fakeQBittorrentList) Remove(context.Context, string) error { return nil }

func TestMagnetInfoHashExtractsHexBTIH(t *testing.T) {
	hash, ok := magnetInfoHash("magnet:?xt=urn:btih:0123456789abcdef0123456789ABCDEF01234567&dn=title")
	if !ok || hash != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("hash=%q ok=%v", hash, ok)
	}
	if _, ok := magnetInfoHash("magnet:?xt=urn:btih:TOOSHORT"); ok {
		t.Fatal("expected a too-short hash to not match")
	}
	if _, ok := magnetInfoHash("https://example.com/some.torrent"); ok {
		t.Fatal("expected a non-magnet link to have no extractable hash")
	}
}

func TestVerifyAddedToQBittorrentMatchesByHash(t *testing.T) {
	s := &Service{}
	qb := fakeQBittorrentList{torrents: []Torrent{{Hash: "0123456789abcdef0123456789abcdef01234567", Name: "some unrelated name"}}}
	hash, ok := s.verifyAddedToQBittorrent(context.Background(), qb, "magnet:?xt=urn:btih:0123456789ABCDEF0123456789ABCDEF01234567", "PRED-001")
	if !ok || hash != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("expected a hash match, got hash=%q ok=%v", hash, ok)
	}
}

func TestVerifyAddedToQBittorrentMatchesByNameWhenNoHashInLink(t *testing.T) {
	s := &Service{}
	qb := fakeQBittorrentList{torrents: []Torrent{{Hash: "deadbeef", Name: "PRED-002 some release title"}}}
	hash, ok := s.verifyAddedToQBittorrent(context.Background(), qb, "https://example.com/some.torrent", "PRED-002")
	if !ok || hash != "deadbeef" {
		t.Fatalf("expected a name match, got hash=%q ok=%v", hash, ok)
	}
}

// TestVerifyAddedToQBittorrentFailsWhenTorrentNeverAppears is the direct
// regression test for the reported bug: qBittorrent can accept an /add
// request and reply "Ok." without ever actually queuing the torrent, and
// that must now be detected instead of trusting the response blindly.
func TestVerifyAddedToQBittorrentFailsWhenTorrentNeverAppears(t *testing.T) {
	s := &Service{}
	qb := fakeQBittorrentList{torrents: nil}
	_, ok := s.verifyAddedToQBittorrent(context.Background(), qb, "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567", "PRED-003")
	if ok {
		t.Fatal("expected no match when the torrent never appears in qBittorrent's list")
	}
}

func TestVerifyAddedToQBittorrentDoesNotClaimExistingManualTorrent(t *testing.T) {
	const existing = "0123456789abcdef0123456789abcdef01234567"
	const added = "abcdef0123456789abcdef0123456789abcdef0123"
	s := &Service{}
	qb := fakeQBittorrentList{torrents: []Torrent{
		{Hash: existing, Name: "SAME-250 manual"},
		{Hash: added, Name: "SAME-250 added by JAVBeacon"},
	}}
	hash, ok := s.verifyAddedToQBittorrent(context.Background(), qb, "https://example.com/new.torrent", "SAME-250", map[string]bool{existing: true})
	if !ok || hash != added {
		t.Fatalf("hash=%q ok=%v; must select new torrent and leave manual torrent alone", hash, ok)
	}
}

func TestVerifyAddedToQBittorrentDoesNotFallBackFromMagnetHashToName(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // After the first observation, stop retries deterministically.
	s := &Service{}
	qb := fakeQBittorrentList{torrents: []Torrent{{Hash: "abcdef0123456789abcdef0123456789abcdef0123", Name: "SAME-250 manual"}}}
	if hash, ok := s.verifyAddedToQBittorrent(ctx, qb, "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567", "SAME-250"); ok {
		t.Fatalf("claimed manual same-name torrent %q", hash)
	}
}

func TestVerifyAddedToQBittorrentRejectsDuplicateManualTorrent(t *testing.T) {
	const hash = "0123456789abcdef0123456789abcdef01234567"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	qb := fakeQBittorrentList{torrents: []Torrent{{Hash: hash, Name: "SAME-250 manual"}}}
	s := &Service{}
	if got, ok := s.verifyAddedToQBittorrent(ctx, qb, "magnet:?xt=urn:btih:"+hash, "SAME-250", map[string]bool{hash: true}); ok {
		t.Fatalf("claimed pre-existing manual torrent %q", got)
	}
}
