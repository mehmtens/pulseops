package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func integrationPool(t *testing.T) *pgxpool.Pool {
	return integrationPoolAt(t, 14)
}

func integrationPoolAt(t *testing.T, targetVersion int64) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("pulseops_test_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE")
		admin.Close()
	})
	if err := migrateTo(ctx, pool, targetVersion); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestUpgradeFromPreviousReleaseReachesHeadAndPreservesData(t *testing.T) {
	pool := integrationPoolAt(t, 12)
	var monitorID int64
	if err := pool.QueryRow(t.Context(), `INSERT INTO monitors(name,url,monitor_type) VALUES('Upgrade sentinel','https://example.com','http') RETURNING id`).Scan(&monitorID); err != nil {
		t.Fatal(err)
	}
	if err := migrate(t.Context(), pool); err != nil {
		t.Fatalf("upgrade migration failed: %v", err)
	}
	var version int
	var name string
	var heartbeatTable bool
	if err := pool.QueryRow(t.Context(), `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT name FROM monitors WHERE id=$1`, monitorID).Scan(&name); err != nil {
		t.Fatalf("pre-upgrade monitor was not preserved: %v", err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT to_regclass('worker_heartbeats') IS NOT NULL`).Scan(&heartbeatTable); err != nil {
		t.Fatal(err)
	}
	if version != 14 || name != "Upgrade sentinel" || !heartbeatTable {
		t.Fatalf("version=%d name=%q worker_heartbeats=%v", version, name, heartbeatTable)
	}
}

func TestMigrationsReachHeadAndAreIdempotent(t *testing.T) {
	pool := integrationPool(t)
	if err := migrate(t.Context(), pool); err != nil {
		t.Fatalf("second migration run failed: %v", err)
	}
	var version int
	if err := pool.QueryRow(t.Context(), "SELECT max(version) FROM schema_migrations").Scan(&version); err != nil || version != 14 {
		t.Fatalf("migration head = %d, err = %v", version, err)
	}
	var schedulesTable bool
	if err := pool.QueryRow(t.Context(), "SELECT to_regclass('maintenance_schedules') IS NOT NULL").Scan(&schedulesTable); err != nil || !schedulesTable {
		t.Fatalf("maintenance_schedules missing: %v", err)
	}
}

func TestWorkerHeartbeatVisibilityAndRegionWarnings(t *testing.T) {
	pool := integrationPool(t)
	token := strings.Repeat("w", 32)
	t.Setenv("PULSEOPS_WORKER_TOKEN", token)
	t.Setenv("PULSEOPS_LOCAL_CHECKS", "false")
	t.Setenv("PULSEOPS_REGION_QUORUM", "2")
	t.Setenv("PULSEOPS_EXPECTED_REGIONS", "eu-west,us-east")
	t.Setenv("PULSEOPS_WORKER_OFFLINE_AFTER_SECONDS", "5")
	application := newApp(pool, strings.Repeat("a", 16))
	claim := httptest.NewRequest(http.MethodPost, "/api/worker/claim", nil)
	claim.Header.Set("Authorization", "Bearer "+token)
	claim.Header.Set("X-PulseOps-Region", "eu-west")
	claimResponse := httptest.NewRecorder()
	application.routes().ServeHTTP(claimResponse, claim)
	if claimResponse.Code != http.StatusNoContent {
		t.Fatalf("empty claim returned %d", claimResponse.Code)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/workers", nil)
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 16))
	response := httptest.NewRecorder()
	application.routes().ServeHTTP(response, request)
	var status struct {
		OnlineRegions int      `json:"onlineRegions"`
		Warnings      []string `json:"warnings"`
		Workers       []struct {
			Region string `json:"region"`
			Online bool   `json:"online"`
			Claims int64  `json:"claims"`
		} `json:"workers"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &status) != nil {
		t.Fatalf("workers returned %d: %s", response.Code, response.Body.String())
	}
	if status.OnlineRegions != 1 || len(status.Workers) != 1 || status.Workers[0].Region != "eu-west" || !status.Workers[0].Online || status.Workers[0].Claims != 1 {
		t.Fatalf("unexpected worker status: %+v", status)
	}
	if len(status.Warnings) != 2 {
		t.Fatalf("warnings=%v, want offline expected region and quorum warning", status.Warnings)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE worker_heartbeats SET last_seen_at=now()-interval '6 seconds' WHERE region='eu-west'`); err != nil {
		t.Fatal(err)
	}
	offlineRequest := httptest.NewRequest(http.MethodGet, "/api/workers", nil)
	offlineRequest.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 16))
	offlineResponse := httptest.NewRecorder()
	application.routes().ServeHTTP(offlineResponse, offlineRequest)
	var offline struct {
		OnlineRegions int `json:"onlineRegions"`
		Workers       []struct {
			Online bool `json:"online"`
		} `json:"workers"`
	}
	if json.Unmarshal(offlineResponse.Body.Bytes(), &offline) != nil || offline.OnlineRegions != 0 || offline.Workers[0].Online {
		t.Fatalf("stale worker was not marked offline: %s", offlineResponse.Body.String())
	}
	recoveryStarted := time.Now()
	recoveryClaim := httptest.NewRequest(http.MethodPost, "/api/worker/claim", nil)
	recoveryClaim.Header.Set("Authorization", "Bearer "+token)
	recoveryClaim.Header.Set("X-PulseOps-Region", "eu-west")
	application.routes().ServeHTTP(httptest.NewRecorder(), recoveryClaim)
	recoveredRequest := httptest.NewRequest(http.MethodGet, "/api/workers", nil)
	recoveredRequest.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 16))
	recoveredResponse := httptest.NewRecorder()
	application.routes().ServeHTTP(recoveredResponse, recoveredRequest)
	var recovered struct {
		OnlineRegions int `json:"onlineRegions"`
	}
	if json.Unmarshal(recoveredResponse.Body.Bytes(), &recovered) != nil || recovered.OnlineRegions != 1 {
		t.Fatalf("worker did not recover on its first claim: %s", recoveredResponse.Body.String())
	}
	t.Logf("worker recovery visible in %s after first claim", time.Since(recoveryStarted))
}

func TestInvitationCreatesIndependentlyRevocableAccess(t *testing.T) {
	pool := integrationPool(t)
	application := newApp(pool, strings.Repeat("a", 16))
	create := httptest.NewRequest(http.MethodPost, "/api/invitations", strings.NewReader(`{"name":"Second operator","scope":"write","expiresInHours":24}`))
	create.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 16))
	createResponse := httptest.NewRecorder()
	application.routes().ServeHTTP(createResponse, create)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create invitation returned %d: %s", createResponse.Code, createResponse.Body.String())
	}
	var invitation struct {
		Token string `json:"token"`
		Path  string `json:"path"`
	}
	if json.Unmarshal(createResponse.Body.Bytes(), &invitation) != nil || invitation.Token == "" || !strings.HasPrefix(invitation.Path, "/#invite=") || strings.Contains(invitation.Path, "?") {
		t.Fatal("invitation token missing")
	}
	accept := httptest.NewRequest(http.MethodPost, "/api/invitations/accept", strings.NewReader(fmt.Sprintf(`{"token":%q}`, invitation.Token)))
	acceptResponse := httptest.NewRecorder()
	application.routes().ServeHTTP(acceptResponse, accept)
	if acceptResponse.Code != http.StatusCreated {
		t.Fatalf("accept returned %d: %s", acceptResponse.Code, acceptResponse.Body.String())
	}
	replay := httptest.NewRequest(http.MethodPost, "/api/invitations/accept", strings.NewReader(fmt.Sprintf(`{"token":%q}`, invitation.Token)))
	replayResponse := httptest.NewRecorder()
	application.routes().ServeHTTP(replayResponse, replay)
	if replayResponse.Code != http.StatusGone {
		t.Fatalf("replayed invitation returned %d, want 410", replayResponse.Code)
	}
	var access struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(acceptResponse.Body.Bytes(), &access)
	var keyID int64
	hash := sha256.Sum256([]byte(access.Token))
	if err := pool.QueryRow(t.Context(), `SELECT id FROM api_keys WHERE token_hash=$1`, hash[:]).Scan(&keyID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM api_keys WHERE id=$1`, keyID); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/monitors", nil)
	request.Header.Set("Authorization", "Bearer "+access.Token)
	response := httptest.NewRecorder()
	application.routes().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked access returned %d, want 401", response.Code)
	}
}

func TestMonitorNotificationPolicyQueuesOnlySelectedChannelAfterDelay(t *testing.T) {
	pool := integrationPool(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	t.Setenv("ALERT_WEBHOOK_URL", server.URL)
	t.Setenv("BREVO_API_KEY", "")
	var monitorID int64
	if err := pool.QueryRow(t.Context(), `INSERT INTO monitors(name,url,monitor_type,failure_threshold,notification_channels,escalation_delay_seconds) VALUES('Scoped API','https://example.com','http',1,ARRAY['webhook'],60) RETURNING id`).Scan(&monitorID); err != nil {
		t.Fatal(err)
	}
	message := "HTTP 503"
	item := dueMonitor{id: monitorID, name: "Scoped API", failureThreshold: 1, recoveryThreshold: 1}
	if err := newApp(pool, strings.Repeat("a", 16)).recordCheck(t.Context(), item, checkResult{up: false, responseMS: 10, message: &message}); err != nil {
		t.Fatal(err)
	}
	var channel string
	var delay float64
	if err := pool.QueryRow(t.Context(), `SELECT channel,EXTRACT(EPOCH FROM next_attempt_at-now()) FROM notification_deliveries WHERE monitor_id=$1`, monitorID).Scan(&channel, &delay); err != nil {
		t.Fatal(err)
	}
	if channel != "webhook" || delay < 55 || delay > 65 {
		t.Fatalf("channel=%s delay=%.1f", channel, delay)
	}
}

func TestExpiredWorkerLeaseRejectsResult(t *testing.T) {
	pool := integrationPool(t)
	var monitorID int64
	if err := pool.QueryRow(t.Context(), `INSERT INTO monitors(name,url,monitor_type) VALUES('API','https://example.com','http') RETURNING id`).Scan(&monitorID); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("w", 32)
	t.Setenv("PULSEOPS_WORKER_TOKEN", token)
	application := newApp(pool, strings.Repeat("a", 16))
	claim := httptest.NewRequest(http.MethodPost, "/api/worker/claim", nil)
	claim.Header.Set("Authorization", "Bearer "+token)
	claim.Header.Set("X-PulseOps-Region", "test-region")
	claimResponse := httptest.NewRecorder()
	application.routes().ServeHTTP(claimResponse, claim)
	if claimResponse.Code != http.StatusOK {
		t.Fatalf("claim returned %d: %s", claimResponse.Code, claimResponse.Body.String())
	}
	var job workerJob
	if err := json.Unmarshal(claimResponse.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE worker_leases SET lease_until=now()-interval '1 second' WHERE monitor_id=$1`, monitorID); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"up":true,"responseMs":10,"lease":%q}`, job.Lease)
	result := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/worker/results/%d", monitorID), strings.NewReader(body))
	result.Header.Set("Authorization", "Bearer "+token)
	result.Header.Set("X-PulseOps-Region", "test-region")
	resultResponse := httptest.NewRecorder()
	application.routes().ServeHTTP(resultResponse, result)
	if resultResponse.Code != http.StatusConflict {
		t.Fatalf("expired lease returned %d, want 409", resultResponse.Code)
	}
	var acceptedResults int64
	if err := pool.QueryRow(t.Context(), `SELECT results FROM worker_heartbeats WHERE region='test-region'`).Scan(&acceptedResults); err != nil {
		t.Fatal(err)
	}
	if acceptedResults != 0 {
		t.Fatalf("expired result incremented accepted results to %d", acceptedResults)
	}
}

func TestFailedNotificationUsesRetryBackoff(t *testing.T) {
	pool := integrationPool(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	t.Setenv("ALERT_WEBHOOK_URL", server.URL)
	t.Setenv("BREVO_API_KEY", "")
	var monitorID int64
	if err := pool.QueryRow(t.Context(), `INSERT INTO monitors(name,url,monitor_type) VALUES('API','https://example.com','http') RETURNING id`).Scan(&monitorID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO notification_deliveries(monitor_id,event_key,subject,body,channel) VALUES($1,'test','Down','Failed','webhook')`, monitorID); err != nil {
		t.Fatal(err)
	}
	newApp(pool, strings.Repeat("a", 16)).deliverNotifications(t.Context())
	var attempts int
	var retrySeconds float64
	if err := pool.QueryRow(t.Context(), `SELECT attempts,EXTRACT(EPOCH FROM next_attempt_at-now()) FROM notification_deliveries WHERE monitor_id=$1`, monitorID).Scan(&attempts, &retrySeconds); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || retrySeconds < 110 || retrySeconds > 125 {
		t.Fatalf("attempts=%d retrySeconds=%.1f, want one attempt and about two minutes", attempts, retrySeconds)
	}
}
