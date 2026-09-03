package main

import (
	"encoding/json"
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

func TestUptimeReportRejectsInvalidRange(t *testing.T) {
	app := newApp(nil, "0123456789abcdef")
	request := httptest.NewRequest(http.MethodGet, "/api/reports/uptime?days=365", nil)
	request.Header.Set("Authorization", "Bearer 0123456789abcdef")
	response := httptest.NewRecorder()
	app.routes().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", response.Code)
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

func TestHeartbeatInputAndContentMatch(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/monitors", strings.NewReader(`{"name":"Nightly backup","monitorType":"heartbeat","intervalSeconds":3600}`))
	input, err := decodeInput(httptest.NewRecorder(), request)
	if err != nil || input.URL != "" || input.MonitorType != "heartbeat" {
		t.Fatalf("unexpected heartbeat: %+v, %v", input, err)
	}
	if !contentMatches([]byte(`{"status":"ok"}`), "status") || contentMatches([]byte("healthy"), "failed") {
		t.Fatal("content matching failed")
	}
}

func TestNetworkMonitorInput(t *testing.T) {
	for _, body := range []string{`{"name":"TLS","monitorType":"tcp","url":"example.com:443"}`, `{"name":"DNS","monitorType":"dns","url":"example.com"}`} {
		request := httptest.NewRequest(http.MethodPost, "/api/monitors", strings.NewReader(body))
		if _, err := decodeInput(httptest.NewRecorder(), request); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/api/monitors", strings.NewReader(`{"name":"Bad","monitorType":"tcp","url":"example.com"}`))
	if _, err := decodeInput(httptest.NewRecorder(), request); err == nil {
		t.Fatal("expected invalid TCP target")
	}
}

func TestOpenAPIDocument(t *testing.T) {
	var document map[string]any
	if json.Unmarshal(openAPIDocument, &document) != nil || document["openapi"] != "3.1.0" {
		t.Fatal("invalid embedded OpenAPI document")
	}
	response := httptest.NewRecorder()
	newApp(nil, "0123456789abcdef").routes().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/openapi.json", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", response.Code)
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
