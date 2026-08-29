package app

import (
	"context"
	"encoding/json"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Net005/JAVBeacon/internal/config"
	"github.com/Net005/JAVBeacon/internal/store"
)

type startupMigrationStatus struct {
	Active    bool      `json:"active"`
	Database  string    `json:"database"`
	Phase     string    `json:"phase"`
	Step      string    `json:"step"`
	Current   int       `json:"current,omitempty"`
	Total     int       `json:"total,omitempty"`
	Attempt   int       `json:"attempt"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type startupMigrationTracker struct {
	mu     sync.RWMutex
	status startupMigrationStatus
}

func newStartupMigrationTracker(cfg config.Config) *startupMigrationTracker {
	now := time.Now().UTC()
	return &startupMigrationTracker{status: startupMigrationStatus{
		Active: true, Database: databaseDescription(cfg), Phase: "Database startup",
		Step: "Waiting to open the database", StartedAt: now, UpdatedAt: now,
	}}
}

func (t *startupMigrationTracker) beginAttempt(attempt int) {
	t.mu.Lock()
	t.status.Active = true
	t.status.Attempt = attempt
	t.status.Phase = "Database startup"
	t.status.Step = "Opening the configured database"
	t.status.Current, t.status.Total = 0, 0
	t.status.UpdatedAt = time.Now().UTC()
	t.mu.Unlock()
}

func (t *startupMigrationTracker) record(progress store.MigrationProgress) {
	t.mu.Lock()
	t.status.Active = true
	t.status.Phase = progress.Phase
	t.status.Step = progress.Step
	t.status.Current = progress.Current
	t.status.Total = progress.Total
	t.status.UpdatedAt = time.Now().UTC()
	t.mu.Unlock()
}

func (t *startupMigrationTracker) snapshot() startupMigrationStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.status
}

func (t *startupMigrationTracker) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/startup/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(t.snapshot())
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = startupProgressTemplate.Execute(w, t.snapshot())
	})
	return mux
}

// serveStartupMigrationProgress temporarily occupies the normal web address
// while New performs synchronous database migrations. It is deliberately
// best-effort: failure to bind is logged but never changes database startup.
func serveStartupMigrationProgress(address string, tracker *startupMigrationTracker, log *slog.Logger) func() {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		log.Warn("could not open temporary database migration progress page", "address", address, "error", err)
		return func() {}
	}
	server := &http.Server{Handler: tracker.handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Warn("database migration progress page stopped", "error", err)
		}
	}()
	return func() {
		ctx, cancel := contextWithShortTimeout()
		defer cancel()
		_ = server.Shutdown(ctx)
	}
}

func contextWithShortTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 3*time.Second)
}

var startupProgressTemplate = template.Must(template.New("startup-progress").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>JAVBeacon · Database upgrade</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
body{font-family:system-ui,sans-serif;background:#0d1015;color:#eef0f4;max-width:720px;margin:0 auto;padding:8vh 24px;line-height:1.5}h1{margin:.2rem 0;font-size:clamp(1.7rem,4vw,2.5rem)}.eyebrow{color:#ff586c;font-weight:800;letter-spacing:.16em}.card{margin-top:28px;padding:24px;border:1px solid #303641;border-radius:14px;background:#141820;box-shadow:0 18px 55px #0006}.muted{color:#9ca5b5}.phase{font-size:1.1rem;font-weight:750}.step{min-height:1.5em;color:#cfd5df}.track{height:10px;overflow:hidden;border-radius:99px;background:#272d38}.bar{height:100%;width:0;background:linear-gradient(90deg,#b31d34,#ff586c);transition:width .35s ease}.meta{display:flex;justify-content:space-between;gap:12px;margin-top:10px;font-size:.85rem;color:#8f98a8}.pulse{display:inline-block;width:9px;height:9px;margin-right:8px;border-radius:50%;background:#ff586c;box-shadow:0 0 0 0 #ff586c88;animation:p 1.6s infinite}@keyframes p{70%{box-shadow:0 0 0 9px #ff586c00}}
</style></head><body>
<div class="eyebrow">JAVBEACON</div><h1>Preparing your database</h1>
<p class="muted">JAVBeacon is connected and applying startup database updates. Keep this page open; the application will load automatically when it is ready.</p>
<div class="card"><div class="phase"><span class="pulse"></span><span id="phase">{{.Phase}}</span></div><p class="step" id="step">{{.Step}}</p><div class="track"><div class="bar" id="bar"></div></div><div class="meta"><span id="count"></span><span id="attempt">Attempt {{.Attempt}}</span></div><p class="muted">{{.Database}}</p></div>
<script>
async function refresh(){try{const r=await fetch('/api/startup/status',{cache:'no-store'});if(!r.headers.get('content-type')?.includes('application/json')){location.reload();return}const s=await r.json();document.getElementById('phase').textContent=s.phase||'Database startup';document.getElementById('step').textContent=s.step||'Working…';const pct=s.total?Math.max(2,Math.min(100,Math.round(s.current/s.total*100))):18;document.getElementById('bar').style.width=pct+'%';document.getElementById('count').textContent=s.total?(s.current+' of '+s.total):'Working…';document.getElementById('attempt').textContent='Attempt '+(s.attempt||1)}catch(e){}}
setInterval(refresh,1000);refresh();
</script></body></html>`))
