package app

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Net005/JAVBeacon/internal/config"
	"github.com/Net005/JAVBeacon/internal/store"
)

// This file implements DB Phase 12 ("PostgreSQL Connection Recovery"):
// what happens when the application is configured for
// JAVBEACON_DB_ENGINE=postgres and the initial connection attempt in
// New (app.go) fails. Before this phase, that error propagated straight
// out of New, main.go logged it and called os.Exit(1) - under Docker's
// "restart: unless-stopped" policy (the DB Phase 1A Compose template) that
// is a silent crash loop with no way to see why or fix it short of
// manually editing docker-compose.yml/.env and hoping.
//
// awaitRecovery instead serves a minimal, unauthenticated recovery
// page/API on the application's normal listen address - the same one a
// healthy instance would serve on - so the operator sees a clear reason
// and a path forward without SSHing in. Per TODO-DATABASE.md's DB Phase 12
// "critical safety behavior", it deliberately does NOT let the user type
// in different connection settings and start running against them: that
// would mean silently overwriting the expected PostgreSQL configuration
// from an unauthenticated page, which the phase explicitly forbids. The
// "Test these settings" form is diagnostic only - it reuses
// store.OpenPostgres/store.ClassifyPostgresError (the same primitives the
// DB Phase 1A/4 setup wizard's Test Connection already uses) purely to
// help the operator figure out what the *real* fix in their actual
// deployment configuration should be. "Retry" always re-attempts the
// exact a.cfg the process was started with - automatically, on a timer,
// and on demand - so a transient problem (PostgreSQL still starting up,
// a brief network blip) resolves itself with no action needed, and a
// deliberate fix (updated docker-compose.yml/.env + container restart)
// is picked up the moment connectivity is restored, without a second
// restart of the JAVBeacon process itself. It never falls back to
// SQLite and never treats the failure as a first-run install.

// recoveryServer holds the mutable state awaitRecovery's HTTP handlers
// read and write while a.store is nil. It is only ever touched by the
// goroutines awaitRecovery itself starts and stops, and its own mutex, so
// it does not need to coordinate with anything on *App.
type recoveryServer struct {
	cfg config.Config

	mu          sync.Mutex
	lastError   error
	lastCheckAt time.Time

	// retryNow is how the HTTP handler asks the awaitRecovery loop to
	// attempt a reconnect immediately instead of waiting for the next
	// timer tick. Buffered so a click is never lost, and a non-blocking
	// send so a second click while one is already queued is a no-op
	// rather than a deadlock.
	retryNow chan struct{}
}

func newRecoveryServer(cfg config.Config, initialErr error) *recoveryServer {
	return &recoveryServer{cfg: cfg, lastError: initialErr, lastCheckAt: time.Now().UTC(), retryNow: make(chan struct{}, 1)}
}

func (r *recoveryServer) recordAttempt(err error) {
	r.mu.Lock()
	r.lastError = err
	r.lastCheckAt = time.Now().UTC()
	r.mu.Unlock()
}

func (r *recoveryServer) requestRetry() {
	select {
	case r.retryNow <- struct{}{}:
	default:
	}
}

// statusPayload is shared by the JSON status endpoint and the HTML page's
// initial render, so both always agree.
type recoveryStatusPayload struct {
	Recovering  bool      `json:"recovering"`
	Database    string    `json:"database"`
	Host        string    `json:"host"`
	Port        int       `json:"port"`
	DatabaseDB  string    `json:"database_name"`
	User        string    `json:"user"`
	SSLMode     string    `json:"sslmode"`
	LastCheckAt time.Time `json:"last_checked_at"`
	Category    string    `json:"category,omitempty"`
	Message     string    `json:"message,omitempty"`
}

func (r *recoveryServer) status() recoveryStatusPayload {
	r.mu.Lock()
	err, checked := r.lastError, r.lastCheckAt
	r.mu.Unlock()
	out := recoveryStatusPayload{
		Recovering:  true,
		Database:    databaseDescription(r.cfg),
		Host:        r.cfg.PostgresHost,
		Port:        r.cfg.PostgresPort,
		DatabaseDB:  r.cfg.PostgresDatabase,
		User:        r.cfg.PostgresUser,
		SSLMode:     r.cfg.PostgresSSLMode,
		LastCheckAt: checked,
	}
	if err != nil {
		category, message := store.ClassifyPostgresError(err)
		out.Category, out.Message = string(category), message
	}
	return out
}

func (r *recoveryServer) mux(log func(string, ...any)) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, 503, map[string]any{"status": "recovering"})
	})
	mux.HandleFunc("GET /api/recovery/status", func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, 200, r.status())
	})
	mux.HandleFunc("POST /api/recovery/retry", func(w http.ResponseWriter, req *http.Request) {
		r.requestRetry()
		writeJSON(w, 200, map[string]any{"queued": true})
	})
	mux.HandleFunc("POST /api/recovery/test", func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Host     string `json:"host"`
			Port     int    `json:"port"`
			Database string `json:"database"`
			User     string `json:"user"`
			Password string `json:"password"`
			SSLMode  string `json:"sslmode"`
		}
		if !decodeJSON(w, req, &body) {
			return
		}
		host := strings.TrimSpace(body.Host)
		database := strings.TrimSpace(body.Database)
		user := strings.TrimSpace(body.User)
		if host == "" || database == "" || user == "" || body.Port < 1 || body.Port > 65535 {
			writeJSON(w, 422, map[string]any{"error": "host, a valid port, database and user are all required"})
			return
		}
		ctx, cancel := context.WithTimeout(req.Context(), 10*time.Second)
		defer cancel()
		db, e := store.OpenPostgres(ctx, store.PostgresConfig{Host: host, Port: body.Port, Database: database, User: user, Password: body.Password, SSLMode: body.SSLMode})
		if e != nil {
			category, message := store.ClassifyPostgresError(e)
			writeJSON(w, 200, map[string]any{"connected": false, "category": string(category), "message": message})
			return
		}
		_ = db.Close()
		writeJSON(w, 200, map[string]any{"connected": true, "message": "connected successfully as " + user + " to database \"" + database + "\""})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		if e := recoveryPageTemplate.Execute(w, r.status()); e != nil {
			log("recovery page render failed", "error", e)
		}
	})
	return mux
}

// awaitRecovery is DB Phase 12's serving loop, run in place of Run's
// normal server loop while a.store is nil. It returns the fully started
// *App the moment a.cfg's PostgreSQL connection succeeds (either via the
// periodic retry or a "Retry now" click), or (nil, nil) if ctx is
// canceled first (a clean shutdown while still waiting).
func (a *App) awaitRecovery(ctx context.Context) (*App, error) {
	rec := newRecoveryServer(a.cfg, a.recoveryErr)
	srv := &http.Server{Addr: a.cfg.ListenAddress, Handler: rec.mux(a.log.Warn), ReadHeaderTimeout: 10 * time.Second}

	errs := make(chan error, 1)
	go func() {
		a.log.Warn("JAVBeacon is running in PostgreSQL connection-recovery mode - see the web UI for details", "address", a.cfg.ListenAddress)
		errs <- srv.ListenAndServe()
	}()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	attempt := func() (*store.SQLite, error) {
		st, e := openStore(a.cfg)
		rec.recordAttempt(e)
		if e != nil {
			a.log.Warn("PostgreSQL connection retry failed", "error", e)
		} else {
			a.log.Info("PostgreSQL connection recovered - resuming normal startup")
		}
		return st, e
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, nil
		case e := <-errs:
			if errors.Is(e, http.ErrServerClosed) {
				return nil, nil
			}
			return nil, e
		case <-ticker.C:
			if st, e := attempt(); e == nil {
				return finishStartup(a.cfg, a.log, a.recoveryLogs, st)
			}
		case <-rec.retryNow:
			if st, e := attempt(); e == nil {
				return finishStartup(a.cfg, a.log, a.recoveryLogs, st)
			}
		}
	}
}

var recoveryPageTemplate = template.Must(template.New("recovery").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>JAVBeacon - Database Connection Problem</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
body{font-family:system-ui,sans-serif;background:#14161a;color:#e6e6e6;max-width:640px;margin:3rem auto;padding:0 1.5rem;line-height:1.5}
h1{font-size:1.3rem}
.card{background:#1e2126;border:1px solid #33373f;border-radius:8px;padding:1.25rem;margin:1rem 0}
.muted{color:#9aa0a8;font-size:0.9rem}
label{display:block;margin:0.5rem 0 0.15rem;font-size:0.85rem}
input,select{width:100%;box-sizing:border-box;padding:0.4rem;border-radius:4px;border:1px solid #33373f;background:#14161a;color:#e6e6e6}
button{margin-top:0.75rem;padding:0.5rem 1rem;border-radius:4px;border:1px solid #33373f;background:#2a2f38;color:#e6e6e6;cursor:pointer}
button:hover{background:#33373f}
.two{display:grid;grid-template-columns:1fr 1fr;gap:0.5rem}
#result,#testResult{margin-top:0.5rem;font-size:0.9rem;white-space:pre-wrap}
</style></head>
<body>
<h1>JAVBeacon can't reach its PostgreSQL database</h1>
<p class="muted">This page is served automatically instead of the application while the configured database is unreachable. It never switches databases or overwrites your configuration - it only helps you see why and confirm a fix. JAVBeacon keeps retrying the configured connection automatically; this page updates itself.</p>

<div class="card">
<p><strong>Configured database:</strong> {{.Database}}</p>
<p id="reason"><strong>Last error:</strong> {{if .Message}}{{.Message}}{{else}}(none yet){{end}}</p>
<p class="muted" id="checked">Last checked: {{.LastCheckAt}}</p>
<button id="retryBtn">Retry now</button>
<div id="result"></div>
</div>

<div class="card">
<p><strong>Test alternate connection settings</strong></p>
<p class="muted">This only tests a connection - it does not change what JAVBeacon actually uses. To make a real change, update JAVBEACON_DB_* in your deployment (e.g. docker-compose.yml / .env) and restart the container.</p>
<div class="two">
<label>Host<input id="tHost" value="{{.Host}}"></label>
<label>Port<input id="tPort" type="number" min="1" max="65535" value="{{.Port}}"></label>
</div>
<div class="two">
<label>Database<input id="tDatabase" value="{{.DatabaseDB}}"></label>
<label>User<input id="tUser" value="{{.User}}"></label>
</div>
<div class="two">
<label>Password<input id="tPassword" type="password"></label>
<label>SSL mode<select id="tSSLMode"><option value="disable">disable</option><option value="prefer">prefer</option><option value="require">require</option><option value="verify-full">verify-full</option></select></label>
</div>
<button id="testBtn">Test these settings</button>
<div id="testResult"></div>
</div>

<script>
document.getElementById('tSSLMode').value = {{.SSLMode}} || 'prefer';
async function api(path, body) {
  const r = await fetch(path, {method: 'POST', headers: {'Content-Type': 'application/json'}, body: body ? JSON.stringify(body) : undefined});
  return r.json();
}
document.getElementById('retryBtn').onclick = async () => {
  document.getElementById('result').textContent = 'Retrying…';
  await api('/api/recovery/retry');
  setTimeout(refresh, 800);
};
document.getElementById('testBtn').onclick = async () => {
  document.getElementById('testResult').textContent = 'Testing…';
  const r = await api('/api/recovery/test', {
    host: document.getElementById('tHost').value,
    port: Number(document.getElementById('tPort').value) || 0,
    database: document.getElementById('tDatabase').value,
    user: document.getElementById('tUser').value,
    password: document.getElementById('tPassword').value,
    sslmode: document.getElementById('tSSLMode').value
  });
  document.getElementById('testResult').textContent = (r.connected ? '✓ ' : '✕ ') + (r.message || r.error || '');
};
async function refresh() {
  try {
    const s = await (await fetch('/api/recovery/status')).json();
    document.getElementById('reason').innerHTML = '<strong>Last error:</strong> ' + (s.message || '(none yet)');
    document.getElementById('checked').textContent = 'Last checked: ' + s.last_checked_at;
    document.getElementById('result').textContent = '';
  } catch (e) {}
}
setInterval(refresh, 5000);
</script>
</body></html>`))

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// decodeJSON decodes req's JSON body into v, writing a 400 Problem
// response and returning false on failure - the recovery mux's own tiny
// counterpart to internal/web.Server.decode, since this package
// deliberately does not depend on internal/web for its minimal degraded-
// mode handlers (see this file's top comment).
func decodeJSON(w http.ResponseWriter, req *http.Request, v any) bool {
	if req.Body == nil {
		writeJSON(w, 400, map[string]any{"error": "a JSON request body is required"})
		return false
	}
	if e := json.NewDecoder(req.Body).Decode(v); e != nil {
		writeJSON(w, 400, map[string]any{"error": "invalid JSON: " + e.Error()})
		return false
	}
	return true
}
