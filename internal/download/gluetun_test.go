package download

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/scraper"
)

func TestGluetunRotatesAtLeastThreeTimesUntilIPChanges(t *testing.T) {
	rotations := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/publicip/ip":
			ip := "198.51.100.1"
			if rotations >= 3 {
				ip = "198.51.100.2"
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"public_ip": ip})
		case "/v1/vpn/status":
			if r.Method == http.MethodPut {
				var body map[string]string
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body["status"] == "running" {
					rotations++
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"outcome": "running"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	r := &gluetunRotator{client: server.Client(), controlURL: server.URL, maxAttempts: 3, waitTimeout: time.Second, pollInterval: time.Millisecond, requireIPChange: true, mu: &sync.Mutex{}}
	oldIP, newIP, attempts, err := r.rotateUntilIPChanges(context.Background())
	if err != nil || oldIP != "198.51.100.1" || newIP != "198.51.100.2" || attempts != 3 || rotations != 3 {
		t.Fatalf("old=%s new=%s attempts=%d rotations=%d err=%v", oldIP, newIP, attempts, rotations, err)
	}
}

func TestJavDB403RotatesThenRetriesBeforeSolver(t *testing.T) {
	directCalls, solverCalls, rotations := 0, 0, 0
	mux := http.NewServeMux()
	mux.HandleFunc("/javdb", func(w http.ResponseWriter, _ *http.Request) {
		directCalls++
		if directCalls == 1 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(w, "<html>recovered directly</html>")
	})
	mux.HandleFunc("/v1/publicip/ip", func(w http.ResponseWriter, _ *http.Request) {
		ip := "198.51.100.1"
		if rotations > 0 {
			ip = "198.51.100.2"
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"public_ip": ip})
	})
	mux.HandleFunc("/v1/vpn/status", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["status"] == "running" {
			rotations++
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"outcome": body["status"]})
	})
	mux.HandleFunc("/solver", func(w http.ResponseWriter, _ *http.Request) {
		solverCalls++
		http.Error(w, "should not run", http.StatusBadGateway)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	pool := scraper.NewSolverPool()
	pool.Configure([]scraper.Instance{{URL: server.URL + "/solver", Priority: 1, Enabled: true}}, 0)
	provider := &javDBProvider{client: server.Client(), solverPool: pool, gluetun: &gluetunRotator{client: server.Client(), controlURL: server.URL, maxAttempts: 3, waitTimeout: time.Second, pollInterval: time.Millisecond, mu: &sync.Mutex{}}}
	doc, status, err := provider.getHTML(context.Background(), server.URL+"/javdb")
	if err != nil || status != http.StatusOK || !strings.Contains(nodeText(doc), "recovered directly") || directCalls != 2 || solverCalls != 0 || rotations != 1 {
		t.Fatalf("status=%d direct=%d solver=%d rotations=%d err=%v", status, directCalls, solverCalls, rotations, err)
	}
}
