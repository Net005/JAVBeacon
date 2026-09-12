package download

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Net005/JAVBeacon/internal/store"
)

func TestPikPakAccountCheckValidatesLoginAndDriveAccess(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "pikpak-check.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	captchaCalls := 0
	client := &http.Client{Transport: pikPakRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.Host == "user.mypikpak.com" && req.URL.Path == "/v1/shield/captcha/init":
			captchaCalls++
			return pikPakJSONResponse(http.StatusOK, `{"captcha_token":"captcha"}`), nil
		case req.URL.Host == "user.mypikpak.com" && req.URL.Path == "/v1/auth/signin":
			return pikPakJSONResponse(http.StatusOK, `{"access_token":"account-token","refresh_token":"refresh-token","expires_in":3600,"sub":"account-id"}`), nil
		case req.URL.Path == "/drive/v1/about":
			if req.Header.Get("Authorization") != "Bearer account-token" {
				t.Fatalf("drive validation omitted account authorization")
			}
			return pikPakJSONResponse(http.StatusOK, `{"kind":"drive#about"}`), nil
		default:
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
			return nil, nil
		}
	})}
	svc := New(st, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.client = client
	result, err := svc.TestPikPakAccount(ctx, "person@example.test", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "passed" || captchaCalls != 2 {
		t.Fatalf("result=%+v captcha_calls=%d", result, captchaCalls)
	}
	settings, err := st.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings["pikpak_check_last_status"] != "passed" || settings["pikpak_check_last_at"] == "" {
		t.Fatalf("persisted status=%v", settings)
	}
	if settings["pikpak_session_refresh_token"] != "refresh-token" || settings["pikpak_session_expires_at"] == "" {
		t.Fatalf("renewable session was not persisted: %v", settings)
	}
	for _, key := range []string{"pikpak_check_last_status", "pikpak_check_last_message"} {
		if strings.Contains(settings[key], "secret") || strings.Contains(settings[key], "account-token") {
			t.Fatal("persisted health status contains a credential or access token")
		}
	}
}

func TestPikPakSessionRefreshesWithoutCredentialLogin(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "pikpak-refresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{
		"pikpak_session_username": "person@example.test", "pikpak_session_refresh_token": "old-refresh",
		"pikpak_session_device_id": "saved-device", "pikpak_session_user_id": "account-id",
	}); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: pikPakRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/v1/auth/token" {
			t.Fatalf("unexpected request: %s", req.URL)
		}
		if req.Header.Get("X-Device-ID") != "saved-device" {
			t.Fatalf("stored device ID was not reused")
		}
		body, _ := io.ReadAll(req.Body)
		if bytes.Contains(body, []byte(`"client_secret"`)) {
			t.Fatalf("public web-client refresh exposed a client secret: %s", body)
		}
		for _, field := range []string{`"client_id"`, `"grant_type":"refresh_token"`, `"refresh_token":"old-refresh"`} {
			if !bytes.Contains(body, []byte(field)) {
				t.Fatalf("refresh body omitted %s: %s", field, body)
			}
		}
		return pikPakJSONResponse(http.StatusOK, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":7200,"sub":"account-id"}`), nil
	})}
	svc := New(st, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.client = client
	session, err := svc.authenticatePikPakSession(ctx, "person@example.test", "unused")
	if err != nil {
		t.Fatal(err)
	}
	if session.accessToken != "new-access" || session.refreshToken != "new-refresh" || session.tokenExpiresAt.IsZero() {
		t.Fatalf("session=%+v", session)
	}
}

func TestPikPakSessionReusesValidAccessToken(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "pikpak-reuse.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{
		"pikpak_session_username":      "person@example.test",
		"pikpak_session_access_token":  "valid-access",
		"pikpak_session_refresh_token": "saved-refresh",
		"pikpak_session_device_id":     "saved-device",
		"pikpak_session_expires_at":    time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: pikPakRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("valid saved session unexpectedly made a token request: %s", req.URL)
		return nil, nil
	})}
	svc := New(st, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.client = client
	session, err := svc.authenticatePikPakSession(ctx, "person@example.test", "unused")
	if err != nil {
		t.Fatal(err)
	}
	if session.accessToken != "valid-access" || session.refreshToken != "saved-refresh" {
		t.Fatalf("session=%+v", session)
	}
}

func TestPushoverPikPakStatusUsesConfiguredKeysAndSafeMessage(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "pushover.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	previousURL := pushoverMessagesURL
	pushoverMessagesURL = "https://push.test/1/messages.json"
	defer func() { pushoverMessagesURL = previousURL }()
	var submitted url.Values
	client := &http.Client{Transport: pikPakRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		submitted, _ = url.ParseQuery(string(body))
		return pikPakJSONResponse(http.StatusOK, `{"status":1}`), nil
	})}
	svc := New(st, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.client = client
	status := PikPakAccountCheck{Status: "failed", Message: "PikPak sign-in failed", CheckedAt: time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)}
	if err := svc.sendPushoverPikPakStatus(context.Background(), map[string]string{"pushover_app_token": "app-token", "pushover_user_key": "user-key"}, status); err != nil {
		t.Fatal(err)
	}
	if submitted.Get("token") != "app-token" || submitted.Get("user") != "user-key" || !strings.Contains(submitted.Get("title"), "failed") || !strings.Contains(submitted.Get("message"), status.Message) {
		t.Fatalf("submitted values=%v", submitted)
	}
}

func TestPikPakScheduleForecastUsesConfiguredInterval(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "pikpak-forecast.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSettings(ctx, map[string]string{"pikpak_check_enabled": "true", "pikpak_check_interval": "12h"}); err != nil {
		t.Fatal(err)
	}
	svc := New(st, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.scheduleNextAttempt["pikpak_account"] = time.Now().Add(time.Hour)
	forecast := svc.PikPakScheduleForecast(ctx)
	if !forecast.Enabled || forecast.Interval != "12h0m0s" || len(forecast.NextRuns) != scheduleForecastRunCount {
		t.Fatalf("forecast=%+v", forecast)
	}
}
