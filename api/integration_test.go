package main

import (
	"context"
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
	if err := migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestMigrationsReachHeadAndAreIdempotent(t *testing.T) {
	pool := integrationPool(t)
	if err := migrate(t.Context(), pool); err != nil {
		t.Fatalf("second migration run failed: %v", err)
	}
	var version int
	if err := pool.QueryRow(t.Context(), "SELECT max(version) FROM schema_migrations").Scan(&version); err != nil || version != 12 {
		t.Fatalf("migration head = %d, err = %v", version, err)
	}
	var schedulesTable bool
	if err := pool.QueryRow(t.Context(), "SELECT to_regclass('maintenance_schedules') IS NOT NULL").Scan(&schedulesTable); err != nil || !schedulesTable {
		t.Fatalf("maintenance_schedules missing: %v", err)
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
