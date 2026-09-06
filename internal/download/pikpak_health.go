package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Net005/JAVBeacon/internal/domain"
)

const defaultPikPakCheckInterval = 24 * time.Hour

var pushoverMessagesURL = "https://api.pushover.net/1/messages.json"

// PikPakAccountCheck is safe to return to the browser and persist in settings:
// it deliberately contains no account identifier, password, token, or provider
// response body.
type PikPakAccountCheck struct {
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	CheckedAt time.Time `json:"checked_at"`
}

func (s *Service) TestPikPakAccount(ctx context.Context, username, password string) (PikPakAccountCheck, error) {
	return s.checkPikPakAccount(ctx, strings.TrimSpace(username), password, "manual")
}

func (s *Service) checkPikPakAccount(ctx context.Context, username, password, source string) (PikPakAccountCheck, error) {
	checkedAt := time.Now().UTC()
	status := PikPakAccountCheck{Status: "failed", CheckedAt: checkedAt}
	if username == "" || password == "" {
		status.Message = "PikPak username and password are required"
		s.persistPikPakCheck(ctx, status)
		return status, errors.New(status.Message)
	}

	s.pikPakCheckMu.Lock()
	if s.pikPakCheckRunning {
		s.pikPakCheckMu.Unlock()
		status.Message = "a PikPak account check is already running"
		return status, errors.New(status.Message)
	}
	s.pikPakCheckRunning = true
	s.pikPakCheckMu.Unlock()
	defer func() {
		s.pikPakCheckMu.Lock()
		s.pikPakCheckRunning = false
		s.pikPakCheckMu.Unlock()
	}()

	client := newPikPakClient(s.client)
	if err := client.login(ctx, username, password); err != nil {
		status.Message = err.Error()
		s.persistPikPakCheck(ctx, status)
		s.log.Warn("PikPak account authentication check failed", "source", source, "checked_at", checkedAt, "error", err)
		return status, err
	}
	if err := client.validateDriveAccess(ctx); err != nil {
		status.Message = err.Error()
		s.persistPikPakCheck(ctx, status)
		s.log.Warn("PikPak account drive-access check failed", "source", source, "checked_at", checkedAt, "error", err)
		return status, err
	}

	status.Status = "passed"
	status.Message = "PikPak sign-in and authenticated drive access passed"
	s.persistPikPakCheck(ctx, status)
	s.log.Info("PikPak account authentication check passed", "source", source, "checked_at", checkedAt)
	return status, nil
}

func (s *Service) persistPikPakCheck(ctx context.Context, status PikPakAccountCheck) {
	if err := s.store.SaveSettings(ctx, map[string]string{
		"pikpak_check_last_status":  status.Status,
		"pikpak_check_last_message": status.Message,
		"pikpak_check_last_at":      status.CheckedAt.Format(time.RFC3339Nano),
	}); err != nil {
		s.log.Warn("PikPak account check result could not be saved", "error", err)
	}
}

func (s *Service) PikPakAccountSchedule(ctx context.Context) {
	lastAttempt := time.Now()
	for {
		settings, _ := s.store.Settings(ctx)
		wait := defaultPikPakCheckInterval
		if parsed, err := domain.ParseScheduleDuration(settings["pikpak_check_interval"]); err == nil && parsed >= time.Minute {
			wait = parsed
		}
		now := time.Now()
		remaining := wait - now.Sub(lastAttempt)
		if remaining <= 0 {
			lastAttempt = now
			if settings["pikpak_check_enabled"] == "true" {
				status, checkErr := s.checkPikPakAccount(ctx, strings.TrimSpace(settings["pikpak_username"]), settings["pikpak_password"], "scheduled")
				notify := (checkErr == nil && settings["pikpak_notify_success"] == "true") || (checkErr != nil && settings["pikpak_notify_failure"] == "true")
				if notify {
					if err := s.sendPushoverPikPakStatus(ctx, settings, status); err != nil {
						s.log.Warn("PikPak account check Pushover notification failed", "check_status", status.Status, "error", err)
					}
				}
			}
			remaining = wait
		}
		s.mu.Lock()
		if s.scheduleNextAttempt == nil {
			s.scheduleNextAttempt = map[string]time.Time{}
		}
		s.scheduleNextAttempt["pikpak_account"] = now.Add(remaining)
		s.mu.Unlock()
		sleep := remaining
		if sleep <= 0 || sleep > scheduleMaxSleepChunk {
			sleep = scheduleMaxSleepChunk
		}
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Service) sendPushoverPikPakStatus(ctx context.Context, settings map[string]string, status PikPakAccountCheck) error {
	token, user := strings.TrimSpace(settings["pushover_app_token"]), strings.TrimSpace(settings["pushover_user_key"])
	if token == "" || user == "" {
		return errors.New("Pushover app token and user/group key are required")
	}
	values := url.Values{
		"token":   {token},
		"user":    {user},
		"title":   {"JAVBeacon · PikPak " + status.Status},
		"message": {status.Message + "\nChecked: " + status.CheckedAt.Format(time.RFC3339)},
	}
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

func (s *Service) PikPakScheduleForecast(ctx context.Context) domain.ScheduleForecast {
	settings, _ := s.store.Settings(ctx)
	return s.intervalScheduleForecast("pikpak_account", "PikPak account check", settings["pikpak_check_enabled"] == "true", settings["pikpak_check_interval"], defaultPikPakCheckInterval)
}
