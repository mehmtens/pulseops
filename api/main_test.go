package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthAndAuth(t *testing.T) {
	app := newApp(nil, "0123456789abcdef")
	health := httptest.NewRecorder()
	app.routes().ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/api/healthz", nil))
	if health.Code != http.StatusOK || health.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("unexpected health response: %d %s", health.Code, health.Body.String())
	}
	unauthorized := httptest.NewRecorder()
	app.routes().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/monitors", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", unauthorized.Code)
	}
}

func TestDecodeInput(t *testing.T) {
	for name, body := range map[string]string{
		"relative URL":   `{"name":"Site","url":"/health"}`,
		"credentials":    `{"name":"Site","url":"https://user:pass@example.com"}`,
		"short interval": `{"name":"Site","url":"https://example.com","intervalSeconds":5}`,
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/monitors", strings.NewReader(body))
			if _, err := decodeInput(httptest.NewRecorder(), request); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	request := httptest.NewRequest(http.MethodPost, "/api/monitors", strings.NewReader(`{"name":"Website","url":"https://example.com"}`))
	input, err := decodeInput(httptest.NewRecorder(), request)
	if err != nil || input.IntervalSeconds != 60 || input.TimeoutSeconds != 10 {
		t.Fatalf("unexpected valid input: %+v, %v", input, err)
	}
}

func TestSafeDialerRejectsPrivateAddress(t *testing.T) {
	_, err := safeDialer()(t.Context(), "tcp", "127.0.0.1:80")
	if err == nil || !strings.Contains(err.Error(), "private or local") {
		t.Fatalf("expected private-address rejection, got %v", err)
	}
}
