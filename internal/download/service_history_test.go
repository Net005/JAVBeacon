package download

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
	"github.com/Net005/JAVBeacon/internal/store"
)

func TestNewRestoresLastDownloadSearchRuns(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := st.SaveDownloadSearchRun(context.Background(), domain.DownloadSearchRun{Schedule: "recent", StartedAt: now.Add(-time.Minute), FinishedAt: now, Checked: 8, Found: 3, Downloaded: 2, Skipped: 6, Failed: 1}); err != nil {
		t.Fatal(err)
	}
	service := New(st, time.Second, slog.Default())
	status := service.SearchStatus()
	if status.Running || !status.FinishedAt.Equal(now) || status.Checked != 8 || status.Found != 3 || status.Downloaded != 2 || status.Failed != 1 {
		t.Fatalf("restored status=%+v", status)
	}
}
