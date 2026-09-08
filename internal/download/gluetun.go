package download

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultGluetunControlURL = "http://127.0.0.1:8000"
	defaultGluetunRotations  = 3
)

type gluetunRotator struct {
	client          *http.Client
	controlURL      string
	apiKey          string
	maxAttempts     int
	waitTimeout     time.Duration
	pollInterval    time.Duration
	settleDelay     time.Duration
	requireIPChange bool
	log             *slog.Logger
	mu              *sync.Mutex
}

func newGluetunRotator(client *http.Client, settings map[string]string, logger *slog.Logger, mu *sync.Mutex) *gluetunRotator {
	if settings["javdb_gluetun_rotation_enabled"] != "true" {
		return nil
	}
	controlURL := strings.TrimRight(strings.TrimSpace(settings["gluetun_control_url"]), "/")
	if controlURL == "" {
		controlURL = defaultGluetunControlURL
	}
	attempts := defaultGluetunRotations
	if parsed, err := strconv.Atoi(strings.TrimSpace(settings["gluetun_rotation_attempts"])); err == nil && parsed > attempts {
		attempts = parsed
	}
	if mu == nil {
		mu = &sync.Mutex{}
	}
	waitSeconds := settingIntAtLeast(settings["gluetun_rotation_wait_seconds"], 45, 5)
	pollMilliseconds := settingIntAtLeast(settings["gluetun_rotation_poll_milliseconds"], 1000, 250)
	settleSeconds := settingIntAtLeast(settings["gluetun_rotation_settle_seconds"], 2, 0)
	return &gluetunRotator{client: client, controlURL: controlURL, apiKey: strings.TrimSpace(settings["gluetun_control_api_key"]), maxAttempts: attempts, waitTimeout: time.Duration(waitSeconds) * time.Second, pollInterval: time.Duration(pollMilliseconds) * time.Millisecond, settleDelay: time.Duration(settleSeconds) * time.Second, requireIPChange: settings["gluetun_require_ip_change"] != "false", log: logger, mu: mu}
}

func settingIntAtLeast(raw string, fallback, minimum int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < minimum {
		return fallback
	}
	return value
}

func (r *gluetunRotator) request(ctx context.Context, method, route string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, r.controlURL+route, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if r.apiKey != "" {
		req.Header.Set("X-API-Key", r.apiKey)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if readErr != nil {
		return readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Gluetun control API HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	if out != nil && len(payload) > 0 {
		if err := json.Unmarshal(payload, out); err != nil {
			return fmt.Errorf("decode Gluetun response: %w", err)
		}
	}
	return nil
}

func (r *gluetunRotator) publicIP(ctx context.Context) (string, error) {
	var response struct {
		PublicIP string `json:"public_ip"`
	}
	if err := r.request(ctx, http.MethodGet, "/v1/publicip/ip", nil, &response); err != nil {
		return "", err
	}
	if strings.TrimSpace(response.PublicIP) == "" {
		return "", errors.New("Gluetun returned an empty public IP")
	}
	return strings.TrimSpace(response.PublicIP), nil
}

func (r *gluetunRotator) setVPN(ctx context.Context, status string) error {
	body, _ := json.Marshal(map[string]string{"status": status})
	return r.request(ctx, http.MethodPut, "/v1/vpn/status", body, nil)
}

func (r *gluetunRotator) waitForPublicIP(ctx context.Context, previous string) (string, error) {
	deadline := time.NewTimer(r.waitTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()
	var lastErr error
	var lastIP string
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline.C:
			if lastIP != "" {
				return lastIP, nil
			}
			return "", fmt.Errorf("wait for Gluetun public IP: %w", lastErr)
		case <-ticker.C:
			ip, err := r.publicIP(ctx)
			if err != nil {
				lastErr = err
				continue
			}
			lastIP = ip
			if !r.requireIPChange || ip != previous {
				return ip, nil
			}
		}
	}
}

func (r *gluetunRotator) rotateUntilIPChanges(ctx context.Context) (oldIP, newIP string, attempts int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	oldIP, err = r.publicIP(ctx)
	if err != nil {
		return "", "", 0, fmt.Errorf("read current Gluetun public IP: %w", err)
	}
	newIP = oldIP
	for attempts = 1; attempts <= r.maxAttempts; attempts++ {
		if err = r.setVPN(ctx, "stopped"); err != nil {
			return oldIP, newIP, attempts, fmt.Errorf("stop Gluetun VPN: %w", err)
		}
		if err = r.setVPN(ctx, "running"); err != nil {
			return oldIP, newIP, attempts, fmt.Errorf("start Gluetun VPN: %w", err)
		}
		newIP, err = r.waitForPublicIP(ctx, oldIP)
		if err != nil {
			return oldIP, newIP, attempts, err
		}
		if newIP != oldIP {
			if r.settleDelay > 0 {
				timer := time.NewTimer(r.settleDelay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return oldIP, newIP, attempts, ctx.Err()
				case <-timer.C:
				}
			}
			return oldIP, newIP, attempts, nil
		}
		if !r.requireIPChange {
			return oldIP, newIP, attempts, nil
		}
		if r.log != nil {
			r.log.Info("Gluetun rotation returned the same public IP; rotating again", "attempt", attempts, "max_attempts", r.maxAttempts, "public_ip", newIP)
		}
	}
	return oldIP, newIP, r.maxAttempts, fmt.Errorf("public IP remained %s after %d rotations", oldIP, r.maxAttempts)
}

// TestGluetunControl checks the same endpoints used by automatic rotation
// without interrupting the VPN tunnel.
func (s *Service) TestGluetunControl(ctx context.Context, controlURL, apiKey string) (string, error) {
	settings := map[string]string{"javdb_gluetun_rotation_enabled": "true", "gluetun_control_url": controlURL, "gluetun_control_api_key": apiKey}
	rotator := newGluetunRotator(s.client, settings, s.log, &s.gluetunRotationMu)
	var status struct {
		Status string `json:"status"`
	}
	if err := rotator.request(ctx, http.MethodGet, "/v1/vpn/status", nil, &status); err != nil {
		return "", err
	}
	if status.Status != "running" {
		return "", fmt.Errorf("Gluetun VPN status is %q", status.Status)
	}
	return rotator.publicIP(ctx)
}
