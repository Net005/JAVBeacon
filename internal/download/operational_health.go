package download

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Net005/JAVBeacon/internal/logging"
	"github.com/Net005/JAVBeacon/internal/scraper"
)

const defaultOperationalHealthInterval = 5 * time.Minute

type byparrHealthState struct {
	Failures int  `json:"failures"`
	Alerted  bool `json:"alerted"`
}

func (s *Service) OperationalHealthSchedule(ctx context.Context) {
	lastAttempt := time.Time{}
	for {
		settings, _ := s.store.Settings(ctx)
		interval := settingDuration(settings["operational_health_interval"], defaultOperationalHealthInterval, time.Minute)
		now := time.Now()
		if lastAttempt.IsZero() || now.Sub(lastAttempt) >= interval {
			lastAttempt = now
			if settings["byparr_health_enabled"] == "true" {
				s.checkByparrInstances(ctx, settings)
			}
			if settings["error_burst_enabled"] == "true" {
				s.checkErrorBurst(ctx, settings, now)
			}
		}
		timer := time.NewTimer(min(interval, scheduleMaxSleepChunk))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func settingDuration(raw string, fallback, minimum time.Duration) time.Duration {
	parsed, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || parsed < minimum {
		return fallback
	}
	return parsed
}

func settingInt(raw string, fallback, minimum int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || parsed < minimum {
		return fallback
	}
	return parsed
}

func byparrHealthURL(instanceURL string) string {
	base := strings.TrimRight(strings.TrimSpace(instanceURL), "/")
	base = strings.TrimSuffix(base, "/v1")
	return base + "/health"
}

func (s *Service) checkByparrInstances(ctx context.Context, settings map[string]string) {
	instances := scraper.ParseInstances(settings["byparr_instances"])
	if len(instances) == 0 && strings.TrimSpace(settings["flaresolverr_url"]) != "" {
		instances = []scraper.Instance{{URL: settings["flaresolverr_url"], Enabled: true, Priority: 1}}
	}
	states := map[string]byparrHealthState{}
	_ = json.Unmarshal([]byte(settings["byparr_health_state"]), &states)
	threshold := settingInt(settings["byparr_health_failure_threshold"], 2, 1)
	timeout := time.Duration(settingInt(settings["byparr_health_timeout_seconds"], 15, 2)) * time.Second
	active := map[string]bool{}
	for _, instance := range instances {
		if !instance.Enabled || strings.TrimSpace(instance.URL) == "" {
			continue
		}
		key := strings.TrimRight(strings.TrimSpace(instance.URL), "/")
		active[key] = true
		state := states[key]
		err := s.checkByparrInstance(ctx, byparrHealthURL(key), timeout)
		if err != nil {
			state.Failures++
			if state.Failures >= threshold && !state.Alerted {
				message := fmt.Sprintf("Byparr instance is unhealthy after %d consecutive checks.\nInstance: %s\nReason: %s", state.Failures, key, err)
				if settings["byparr_notify_failure"] == "true" {
					if pushErr := s.sendPushover(ctx, settings, "pushover_byparr_app_token", "JAVBeacon · Byparr unhealthy", message); pushErr != nil {
						s.log.Warn("Byparr health Pushover notification failed", "instance", key, "error", pushErr)
					} else {
						state.Alerted = true
					}
				}
				// Latch the incident even when Pushover itself is unavailable. A
				// notification delivery failure must never become a retry flood.
				state.Alerted = true
				s.log.Warn("Byparr instance health threshold reached", "instance", key, "consecutive_failures", state.Failures, "error", err)
			}
		} else {
			if state.Alerted && settings["byparr_notify_recovery"] == "true" {
				if pushErr := s.sendPushover(ctx, settings, "pushover_byparr_app_token", "JAVBeacon · Byparr recovered", "Byparr instance is healthy again.\nInstance: "+key); pushErr != nil {
					s.log.Warn("Byparr recovery Pushover notification failed", "instance", key, "error", pushErr)
				}
			}
			if state.Failures > 0 || state.Alerted {
				s.log.Info("Byparr instance health recovered", "instance", key)
			}
			state = byparrHealthState{}
		}
		states[key] = state
	}
	for key := range states {
		if !active[key] {
			delete(states, key)
		}
	}
	encoded, _ := json.Marshal(states)
	_ = s.store.SaveSettings(ctx, map[string]string{"byparr_health_state": string(encoded), "byparr_health_last_at": time.Now().UTC().Format(time.RFC3339Nano)})
}

func (s *Service) checkByparrInstance(ctx context.Context, healthURL string, timeout time.Duration) error {
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(checkCtx, http.MethodGet, healthURL, nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (s *Service) checkErrorBurst(ctx context.Context, settings map[string]string, now time.Time) {
	if s.logs == nil {
		return
	}
	window := settingDuration(settings["error_burst_window"], 10*time.Minute, time.Minute)
	cooldown := settingDuration(settings["error_burst_cooldown"], 6*time.Hour, 5*time.Minute)
	threshold := settingInt(settings["error_burst_threshold"], 10, 3)
	counts := map[string]int{"scraping": 0, "http_search": 0, "http_download": 0}
	weights := map[string]int{
		"scraping":      settingInt(settings["error_burst_weight_scraping"], 1, 1),
		"http_search":   settingInt(settings["error_burst_weight_http_search"], 2, 1),
		"http_download": settingInt(settings["error_burst_weight_http_download"], 3, 1),
	}
	seen := map[string]bool{}
	for _, entry := range s.logs.Entries(0) {
		if entry.Time.Before(now.Add(-window)) {
			continue
		}
		if category := operationalErrorCategory(entry); category != "" && settings["error_burst_include_"+category] != "false" && !seen[operationalErrorIdentity(category, entry)] {
			seen[operationalErrorIdentity(category, entry)] = true
			counts[category]++
		}
	}
	total := counts["scraping"]*weights["scraping"] + counts["http_search"]*weights["http_search"] + counts["http_download"]*weights["http_download"]
	active := settings["error_burst_alert_active"] == "true"
	if total >= threshold && !active {
		message := fmt.Sprintf("Operational failure score %d reached the threshold of %d within %s.\nScraping: %d × %d\nHTTP search: %d × %d\nHTTP downloads: %d × %d", total, threshold, window, counts["scraping"], weights["scraping"], counts["http_search"], weights["http_search"], counts["http_download"], weights["http_download"])
		lastAlert, _ := time.Parse(time.RFC3339Nano, settings["error_burst_last_alert_at"])
		canNotify := lastAlert.IsZero() || now.Sub(lastAlert) >= cooldown
		if canNotify && settings["error_burst_notify"] == "true" {
			if pushErr := s.sendPushover(ctx, settings, "pushover_download_search_app_token", "JAVBeacon · Download + Search errors", message); pushErr != nil {
				s.log.Warn("Download + Search error Pushover notification failed; incident latched without retrying", "error", pushErr)
			}
		}
		values := map[string]string{"error_burst_alert_active": "true"}
		if canNotify {
			values["error_burst_last_alert_at"] = now.UTC().Format(time.RFC3339Nano)
		}
		_ = s.store.SaveSettings(ctx, values)
		s.log.Warn("operational error burst threshold reached", "window", window, "threshold", threshold, "total", total, "scraping", counts["scraping"], "http_search", counts["http_search"], "http_download", counts["http_download"], "notification_suppressed_by_cooldown", !canNotify)
	} else if total < threshold && active {
		_ = s.store.SaveSettings(ctx, map[string]string{"error_burst_alert_active": "false"})
		s.log.Info("operational error burst recovered", "window", window, "total", total)
	}
	encoded, _ := json.Marshal(counts)
	_ = s.store.SaveSettings(ctx, map[string]string{"error_burst_last_counts": string(encoded), "error_burst_last_at": now.UTC().Format(time.RFC3339Nano)})
}

// operationalErrorIdentity collapses retry noise into one logical incident.
// It deliberately prefers stable release/URL identifiers over the error text,
// which often changes slightly between Cloudflare or solver attempts.
func operationalErrorIdentity(category string, entry logging.Entry) string {
	parts := []string{category}
	for _, key := range []string{"release_id", "video_id", "url", "detail_url", "search_url", "site", "provider"} {
		if value := strings.TrimSpace(fmt.Sprint(entry.Fields[key])); value != "" {
			parts = append(parts, key+"="+strings.ToLower(value))
		}
	}
	if len(parts) == 1 {
		parts = append(parts, strings.ToLower(entry.Message))
	}
	return strings.Join(parts, "|")
}

func operationalErrorCategory(entry logging.Entry) string {
	message := strings.ToLower(entry.Message)
	level := strings.ToUpper(entry.Level)
	if strings.Contains(message, "operational error burst") || strings.Contains(message, "pushover notification failed") {
		return ""
	}
	if strings.Contains(message, "http provider search failed") {
		return "http_search"
	}
	if strings.Contains(message, "download failed") && strings.EqualFold(fmt.Sprint(entry.Fields["transport"]), "http") {
		return "http_download"
	}
	isScrapingFailure := strings.Contains(message, "scrape") ||
		strings.Contains(message, "refresh failed") ||
		strings.Contains(message, "release detail") ||
		strings.Contains(message, "product detail failed") ||
		strings.Contains(message, "historical index directory unavailable")
	// Scrapers such as JavLibrary report individual product/index failures as
	// warnings so a partially successful scrape can continue. They are still
	// real failures and contribute once per release/URL after retry collapsing.
	if (level == "WARN" || level == "ERROR") && isScrapingFailure {
		return "scraping"
	}
	return ""
}

func (s *Service) sendPushover(ctx context.Context, settings map[string]string, tokenSetting, title, message string) error {
	token, user := strings.TrimSpace(settings[tokenSetting]), strings.TrimSpace(settings["pushover_user_key"])
	if token == "" || user == "" {
		return fmt.Errorf("Pushover app token and user/group key are required")
	}
	values := url.Values{"token": {token}, "user": {user}, "title": {title}, "message": {message}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, pushoverMessagesURL, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("send Pushover notification: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("Pushover returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (s *Service) TestPushoverCategory(ctx context.Context, userKey, appToken, category string) error {
	category = strings.TrimSpace(category)
	if category == "" {
		category = "Health"
	}
	return s.sendPushover(ctx, map[string]string{"pushover_user_key": userKey, "test_app_token": appToken}, "test_app_token", "JAVBeacon · "+category+" test", "Test notification delivered successfully from JAVBeacon.")
}
