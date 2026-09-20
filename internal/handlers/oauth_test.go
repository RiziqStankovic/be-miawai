package handlers

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"be-miawai/internal/config"
)

func TestExchangeTokenLogsSafeJSONError(t *testing.T) {
	secret := "client-secret-value"
	code := "authorization-code-value"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"authorization code rejected"}`)
	}))
	defer server.Close()

	var logs bytes.Buffer
	originalWriter := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(originalWriter)

	s := NewServer(config.Config{}, nil)
	_, err := s.exchangeToken(context.Background(), server.URL, "google", "token_exchange", url.Values{
		"code":          {code},
		"client_secret": {secret},
	})
	if err == nil {
		t.Fatal("expected token exchange error")
	}
	if strings.Contains(err.Error(), code) || strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked sensitive value: %q", err)
	}
	assertSafeOAuthLog(t, logs.String(), code, secret)
	if !strings.Contains(logs.String(), "provider=google") || !strings.Contains(logs.String(), "endpoint_class=token_exchange") || !strings.Contains(logs.String(), "status=400") || !strings.Contains(logs.String(), "error_code=\"invalid_grant\"") {
		t.Fatalf("missing structured diagnostics: %q", logs.String())
	}
}

func TestExchangeTokenLogsSafeNonJSONError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "upstream failure")
	}))
	defer server.Close()

	var logs bytes.Buffer
	originalWriter := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(originalWriter)

	s := NewServer(config.Config{}, nil)
	_, err := s.exchangeToken(context.Background(), server.URL, "google", "token_exchange", nil)
	if err == nil {
		t.Fatal("expected token exchange error")
	}
	if strings.Contains(err.Error(), "upstream failure") {
		t.Fatalf("error leaked provider body: %q", err)
	}
	if !strings.Contains(logs.String(), "status=502") || !strings.Contains(logs.String(), "error_code=\"invalid_response\"") {
		t.Fatalf("missing non-json diagnostics: %q", logs.String())
	}
}

func TestUserinfoFailureDoesNotLeakAccessToken(t *testing.T) {
	token := "access-token-value"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"invalid_token","error_description":"token rejected"}`)
	}))
	defer server.Close()

	var logs bytes.Buffer
	originalWriter := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(originalWriter)

	s := NewServer(config.Config{}, nil)
	var profile struct {
		Sub string `json:"sub"`
	}
	err := s.getJSON(context.Background(), server.URL, "google", "userinfo", token, &profile)
	if err == nil {
		t.Fatal("expected userinfo error")
	}
	if strings.Contains(err.Error(), token) || strings.Contains(logs.String(), token) {
		t.Fatalf("access token leaked: error=%q logs=%q", err, logs.String())
	}
	if !strings.Contains(logs.String(), "endpoint_class=userinfo") || !strings.Contains(logs.String(), "status=401") {
		t.Fatalf("missing userinfo diagnostics: %q", logs.String())
	}
}

func TestExchangeTokenNetworkErrorDoesNotLeakRequestValues(t *testing.T) {
	secret := "client-secret-value"
	code := "authorization-code-value"
	var logs bytes.Buffer
	originalWriter := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(originalWriter)

	s := NewServer(config.Config{}, nil)
	s.client = &http.Client{}
	_, err := s.exchangeToken(context.Background(), "http://127.0.0.1:1/token", "google", "token_exchange", url.Values{
		"code":          {code},
		"client_secret": {secret},
	})
	if err == nil {
		t.Fatal("expected network error")
	}
	assertSafeOAuthLog(t, logs.String(), code, secret)
	if !strings.Contains(logs.String(), "status=0") || !strings.Contains(logs.String(), "network failure") {
		t.Fatalf("missing network diagnostics: %q", logs.String())
	}
}

func TestGoogleAuthURLUsesAPIBaseURLCallback(t *testing.T) {
	s := NewServer(config.Config{APIBaseURL: "https://api.example.test", GoogleClientID: "client-id"}, nil)
	authURL, err := s.buildAuthURL("google", "state-value")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Query().Get("redirect_uri"); got != "https://api.example.test/v1/auth/google/callback" {
		t.Fatalf("unexpected redirect URI: %q", got)
	}
}

func assertSafeOAuthLog(t *testing.T, logs string, code string, secret string) {
	t.Helper()
	if strings.Contains(logs, code) || strings.Contains(logs, secret) || strings.Contains(logs, "access-token") || strings.Contains(logs, "refresh-token") {
		t.Fatalf("sensitive value leaked in logs: %q", logs)
	}
}
