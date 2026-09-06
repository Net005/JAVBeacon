package download

import (
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
			return pikPakJSONResponse(http.StatusOK, `{"access_token":"account-token","sub":"account-id"}`), nil
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
	for _, value := range settings {
		if strings.Contains(value, "secret") || strings.Contains(value, "account-token") {
			t.Fatal("persisted health status contains a credential or access token")
		}
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
