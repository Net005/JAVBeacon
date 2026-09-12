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

const (
	defaultPikPakCheckInterval = 24 * time.Hour
	pikPakTokenRefreshSkew     = 5 * time.Minute
)

var pushoverMessagesURL = "https://api.pushover.net/1/messages.json"

// PikPakAccountCheck is safe to return to the browser and persist in settings:
// it deliberately contains no account identifier, password, token, or provider
// response body.
type PikPakAccountCheck struct {
	Status           string    `json:"status"`
	Message          string    `json:"message"`
	CheckedAt        time.Time `json:"checked_at"`
	SessionIssuedAt  time.Time `json:"session_issued_at,omitempty"`
	SessionExpiresAt time.Time `json:"session_expires_at,omitempty"`
	ReauthRequired   bool      `json:"reauth_required"`
	VerificationURL  string    `json:"verification_url,omitempty"`
}

func (s *Service) TestPikPakAccount(ctx context.Context, username, password string) (PikPakAccountCheck, error) {
	return s.checkPikPakAccount(ctx, strings.TrimSpace(username), password, "manual", true)
}

func (s *Service) checkPikPakAccount(ctx context.Context, username, password, source string, forceLogin bool) (PikPakAccountCheck, error) {
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

	var client *pikPakClient
	var err error
	if forceLogin {
		client, err = s.reauthenticatePikPakSession(ctx, username, password)
	} else {
		client, err = s.authenticatePikPakSession(ctx, username, password)
	}
	if err != nil {
		status.Status = "reauth_required"
		status.Message = err.Error()
		status.ReauthRequired = true
		if client != nil {
			status.VerificationURL = client.verificationURL
		}
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
	status.Message = "PikPak session and authenticated drive access passed"
	status.SessionIssuedAt = client.tokenIssuedAt
	status.SessionExpiresAt = client.tokenExpiresAt
	s.persistPikPakCheck(ctx, status)
	s.log.Info("PikPak account authentication check passed", "source", source, "checked_at", checkedAt)
	return status, nil
}

func (s *Service) authenticatePikPakSession(ctx context.Context, username, password string) (*pikPakClient, error) {
	return s.pikPakSession(ctx, strings.TrimSpace(username), password, false)
}

func (s *Service) reauthenticatePikPakSession(ctx context.Context, username, password string) (*pikPakClient, error) {
	return s.pikPakSession(ctx, strings.TrimSpace(username), password, true)
}

func (s *Service) pikPakSession(ctx context.Context, username, password string, forceLogin bool) (*pikPakClient, error) {
	s.pikPakSessionMu.Lock()
	defer s.pikPakSessionMu.Unlock()

	settings, _ := s.store.Settings(ctx)
	client := newPikPakClient(s.client)
	if saved := strings.TrimSpace(settings["pikpak_session_device_id"]); saved != "" {
		client.deviceID = saved
	}
	client.username = username
	client.userID = settings["pikpak_session_user_id"]
	client.accessToken = settings["pikpak_session_access_token"]
	client.refreshToken = settings["pikpak_session_refresh_token"]
	client.tokenIssuedAt, _ = time.Parse(time.RFC3339Nano, settings["pikpak_session_issued_at"])
	client.tokenExpiresAt, _ = time.Parse(time.RFC3339Nano, settings["pikpak_session_expires_at"])

	matchingAccount := username != "" && username == settings["pikpak_session_username"]
	if !forceLogin && matchingAccount && client.accessToken != "" && client.tokenExpiresAt.After(time.Now().Add(pikPakTokenRefreshSkew)) {
		s.clearPikPakReauthState(ctx)
		return client, nil
	}
	if !forceLogin && matchingAccount && client.refreshToken != "" {
		if err := client.refreshLogin(ctx); err == nil {
			if saveErr := s.persistPikPakSession(ctx, client, username); saveErr != nil {
				return client, saveErr
			}
			s.clearPikPakReauthState(ctx)
			s.log.Info("PikPak session refreshed automatically", "session_expires_at", formatPikPakTime(client.tokenExpiresAt))
			return client, nil
		} else {
			s.log.Warn("PikPak saved session refresh failed; attempting credential sign-in", "error", redactPikPakAuthError(err, username, password))
		}
	}

	if username == "" || password == "" {
		err := errors.New("PikPak re-authentication required: saved session is unavailable and credentials are incomplete")
		s.recordPikPakReauthRequired(ctx, settings, client, err)
		return client, err
	}
	if err := client.login(ctx, username, password); err != nil {
		err = fmt.Errorf("PikPak automatic re-authentication failed: %w", redactPikPakAuthError(err, username, password))
		s.recordPikPakReauthRequired(ctx, settings, client, err)
		return client, err
	}
	if err := s.persistPikPakSession(ctx, client, username); err != nil {
		return client, err
	}
	s.clearPikPakReauthState(ctx)
	s.log.Info("PikPak session authenticated and stored", "session_expires_at", formatPikPakTime(client.tokenExpiresAt))
	return client, nil
}

func (s *Service) persistPikPakSession(ctx context.Context, client *pikPakClient, username string) error {
	return s.store.SaveSettings(ctx, map[string]string{
		"pikpak_session_username":      username,
		"pikpak_session_device_id":     client.deviceID,
		"pikpak_session_user_id":       client.userID,
		"pikpak_session_access_token":  client.accessToken,
		"pikpak_session_refresh_token": client.refreshToken,
		"pikpak_session_issued_at":     formatPikPakTime(client.tokenIssuedAt),
		"pikpak_session_expires_at":    formatPikPakTime(client.tokenExpiresAt),
	})
}

func formatPikPakTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func (s *Service) clearPikPakReauthState(ctx context.Context) {
	_ = s.store.SaveSettings(ctx, map[string]string{
		"pikpak_reauth_required": "false", "pikpak_verification_url": "", "pikpak_reauth_alert_active": "false",
	})
}

func (s *Service) recordPikPakReauthRequired(ctx context.Context, settings map[string]string, client *pikPakClient, authErr error) {
	verificationURL := ""
	if client != nil {
		verificationURL = client.verificationURL
	}
	status := PikPakAccountCheck{Status: "reauth_required", Message: authErr.Error(), CheckedAt: time.Now().UTC(), ReauthRequired: true, VerificationURL: verificationURL}
	s.persistPikPakCheck(ctx, status)
	s.log.Warn("PikPak requires re-authentication", "verification_url", verificationURL, "error", authErr)
	if settings["pikpak_notify_failure"] == "true" && settings["pikpak_reauth_alert_active"] != "true" {
		if err := s.sendPushoverPikPakStatus(ctx, settings, status); err != nil {
			s.log.Warn("PikPak re-authentication Pushover notification failed", "error", err)
		} else {
			_ = s.store.SaveSettings(ctx, map[string]string{"pikpak_reauth_alert_active": "true"})
		}
	}
}

func (s *Service) persistPikPakCheck(ctx context.Context, status PikPakAccountCheck) {
	values := map[string]string{
		"pikpak_check_last_status":  status.Status,
		"pikpak_check_last_message": status.Message,
		"pikpak_check_last_at":      status.CheckedAt.Format(time.RFC3339Nano),
		"pikpak_reauth_required":    fmt.Sprintf("%t", status.ReauthRequired),
		"pikpak_verification_url":   status.VerificationURL,
	}
	if !status.SessionIssuedAt.IsZero() {
		values["pikpak_session_issued_at"] = formatPikPakTime(status.SessionIssuedAt)
	}
	if !status.SessionExpiresAt.IsZero() {
		values["pikpak_session_expires_at"] = formatPikPakTime(status.SessionExpiresAt)
	}
	if err := s.store.SaveSettings(ctx, values); err != nil {
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
				status, checkErr := s.checkPikPakAccount(ctx, strings.TrimSpace(settings["pikpak_username"]), settings["pikpak_password"], "scheduled", false)
				notify := (checkErr == nil && settings["pikpak_notify_success"] == "true") || (checkErr != nil && !status.ReauthRequired && settings["pikpak_notify_failure"] == "true")
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
	token := strings.TrimSpace(settings["pushover_pikpak_app_token"])
	if token == "" {
		token = strings.TrimSpace(settings["pushover_app_token"])
	}
	user := strings.TrimSpace(settings["pushover_user_key"])
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
