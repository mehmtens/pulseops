package main

import (
	"io"
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

func TestTransitionThresholdsAndMaintenance(t *testing.T) {
	for name, test := range map[string]struct {
		up, maintenance     bool
		failures, successes int
		open, resolve       bool
	}{
		"first failure is tolerated": {false, false, 1, 0, false, false},
		"second failure opens":       {false, false, 2, 0, true, false},
		"second success resolves":    {true, false, 0, 2, false, true},
		"maintenance suppresses":     {false, true, 9, 0, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			open, resolve := transition(test.up, test.maintenance, test.failures, test.successes, 2, 2)
			if open != test.open || resolve != test.resolve {
				t.Fatalf("got open=%v resolve=%v", open, resolve)
			}
		})
	}
}

func TestSendWebhook(t *testing.T) {
	received := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- string(body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	t.Setenv("ALERT_WEBHOOK_URL", server.URL)
	if err := sendWebhook("API down", "HTTP 503"); err != nil {
		t.Fatal(err)
	}
	if body := <-received; !strings.Contains(body, `"event":"pulseops.alert"`) || !strings.Contains(body, "API down") {
		t.Fatalf("unexpected payload: %s", body)
	}
}
