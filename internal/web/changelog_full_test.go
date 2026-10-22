package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFullChangelogEndpoint(t *testing.T) {
	s := &Server{log: slog.Default()}
	rec := httptest.NewRecorder()
	s.fullChangelog(rec, httptest.NewRequest(http.MethodGet, "/api/changelog", nil))
	var got struct {
		Current  string `json:"current"`
		Releases []struct {
			Version string `json:"version"`
		} `json:"releases"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || len(got.Releases) < 2 || got.Current == "" {
		t.Fatalf("status=%d body=%.200s", rec.Code, rec.Body.String())
	}
}
