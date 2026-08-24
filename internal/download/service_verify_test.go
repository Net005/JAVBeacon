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
