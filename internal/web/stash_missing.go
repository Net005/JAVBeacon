package web

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Net005/JAVBeacon/internal/domain"
)

// stashMissingFilterFromQuery mirrors releaseFilterFromQuery for the
// Missing Library Files section (TODO-2.0 Phase 2).
func stashMissingFilterFromQuery(q url.Values) domain.StashMissingFilter {
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	return domain.StashMissingFilter{
		Status: q.Get("status"), SearchExpression: q.Get("search_expression"),
		Sort: q.Get("sort"), Direction: q.Get("direction"),
		Limit: limit, Offset: offset,
	}
}

func (s *Server) stashMissingList(w http.ResponseWriter, r *http.Request) {
	x, e := s.store.StashMissingScenes(r.Context(), stashMissingFilterFromQuery(r.URL.Query()))
	if e != nil {
		s.problem(w, 500, e.Error())
		return
	}
	s.json(w, 200, x)
}

func (s *Server) stashMissingCount(w http.ResponseWriter, r *http.Request) {
	total, e := s.store.StashMissingScenesCount(r.Context(), stashMissingFilterFromQuery(r.URL.Query()))
	if e != nil {
		s.problem(w, 500, e.Error())
		return
	}
	s.json(w, 200, map[string]any{"total": total})
}

// stashMissingClear handles the manual "Clear results" action: it wipes
// every recorded Missing Library Files row, refusing (409) while a scan is
// actively running to avoid racing with its writes.
func (s *Server) stashMissingClear(w http.ResponseWriter, r *http.Request) {
	n, e := s.stash.ClearMissingScenes(r.Context())
	if e != nil {
		s.problem(w, http.StatusConflict, e.Error())
		return
	}
	s.json(w, 200, map[string]any{"removed": n})
}

func (s *Server) stashMissingScanJob(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.json(w, 200, s.stash.MissingScanStatus())
		return
	}
	if e := s.stash.StartMissingScan(r.Context()); e != nil {
		s.problem(w, http.StatusConflict, e.Error())
		return
	}
	s.json(w, http.StatusAccepted, s.stash.MissingScanStatus())
}

// stashMissingIDsRequest is the shared POST body shape for the retrieve and
// apply bulk actions below: a list of stash_missing_scenes row IDs the user
// selected in the Missing Library Files section.
type stashMissingIDsRequest struct {
	IDs  []int64 `json:"ids"`
	Mode string  `json:"mode,omitempty"`
	// AllowNonPreferredFilenames only applies to the apply-job endpoint
	// (mode monitor_download): when true it enables the TODO-2.0 Task A
	// fallback chain in download.Service.SearchAndDownloadNow, accepting a
	// seeded-but-unaccepted, or failing that merely most-recent, result
	// instead of requiring a clean accepted-filename-pattern match. Ignored
	// by the retrieve endpoint.
	AllowNonPreferredFilenames bool `json:"allow_non_preferred_filenames,omitempty"`
}

func (s *Server) stashMissingRetrieveJob(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.json(w, 200, s.stash.RetrieveScanStatus())
		return
	}
	var body stashMissingIDsRequest
	if !s.decode(w, r, &body) {
		return
	}
	if len(body.IDs) == 0 {
		s.problem(w, http.StatusUnprocessableEntity, "select at least one release to retrieve")
		return
	}
	if e := s.stash.StartRetrieve(r.Context(), body.IDs); e != nil {
		s.problem(w, http.StatusConflict, e.Error())
		return
	}
	s.json(w, http.StatusAccepted, s.stash.RetrieveScanStatus())
}

func (s *Server) stashMissingApplyJob(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.json(w, 200, s.stash.ApplyRunStatus())
		return
	}
	var body stashMissingIDsRequest
	if !s.decode(w, r, &body) {
		return
	}
	if len(body.IDs) == 0 {
		s.problem(w, http.StatusUnprocessableEntity, "select at least one release")
		return
	}
	switch body.Mode {
	case "", "monitor_only":
		body.Mode = "monitor_only"
	case "monitor_download":
	default:
		s.problem(w, http.StatusUnprocessableEntity, "mode must be monitor_only or monitor_download")
		return
	}
	if e := s.stash.StartApply(r.Context(), body.IDs, body.Mode, body.AllowNonPreferredFilenames); e != nil {
		s.problem(w, http.StatusConflict, e.Error())
		return
	}
	s.json(w, http.StatusAccepted, s.stash.ApplyRunStatus())
}

// browseDirEntry is one directory listed by browseDir - only directories are
// returned since this control exists solely to pick the disk root(s) used
// by the Missing Library Files path remap (a folder path, not a file).
type browseDirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// browseDir lists subdirectories of a given path (default "/") for the
// Settings "Browse" control used to fill in each stash_missing_path_remaps
// row's "to" path without the user hand-typing an absolute path. It
// intentionally never lists file contents or reads outside directory
// listing - this is a single-user admin app, so the only guard needed is
// against a completely invalid path silently doing nothing useful.
func (s *Server) browseDir(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if strings.TrimSpace(path) == "" {
		path = "/"
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		s.problem(w, http.StatusNotFound, "not a directory: "+path)
		return
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		s.problem(w, 500, err.Error())
		return
	}
	dirs := make([]browseDirEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		dirs = append(dirs, browseDirEntry{Name: entry.Name(), Path: filepath.Join(path, entry.Name())})
	}
	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name) })
	parent := filepath.Dir(path)
	if parent == path {
		parent = ""
	}
	s.json(w, 200, map[string]any{"path": path, "parent": parent, "directories": dirs})
}
