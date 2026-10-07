package web

import (
	"errors"
	"github.com/Net005/JAVBeacon/internal/covers"
	"io"
	"net/http"
)

// Protected by the application's existing API-key/session middleware.
func (s *Server) siloRenderPoster(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
	if err != nil {
		s.problem(w, http.StatusRequestEntityTooLarge, "poster source exceeds 16 MiB")
		return
	}
	result, err := covers.RenderSceneCrop(r.Context(), raw)
	if errors.Is(err, covers.ErrInsufficientContext) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		s.problem(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Silo-Poster-Layout", "yunet-context-v2")
	_, _ = w.Write(result)
}

// Future metadata refreshes use the same composition as the repair worker.
// Already-tight originals remain unchanged; renderer failures are visible.
func (s *Server) serveSiloContextPoster(w http.ResponseWriter, r *http.Request, resp *http.Response) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if err != nil || len(raw) > 16<<20 {
		s.problem(w, http.StatusBadGateway, "invalid cover image")
		return
	}
	result, err := covers.RenderSceneCrop(r.Context(), raw)
	if errors.Is(err, covers.ErrInsufficientContext) {
		result = raw
		err = nil
	}
	if err != nil {
		s.problem(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", http.DetectContentType(result))
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(result)
}
