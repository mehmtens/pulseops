package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

type workerJob struct {
	ID                int64  `json:"id"`
	Name              string `json:"name"`
	URL               string `json:"url"`
	MonitorType       string `json:"monitorType"`
	ExpectedKeyword   string `json:"expectedKeyword"`
	TimeoutSeconds    int    `json:"timeoutSeconds"`
	IntervalSeconds   int    `json:"intervalSeconds"`
	FailureThreshold  int    `json:"failureThreshold"`
	RecoveryThreshold int    `json:"recoveryThreshold"`
	Maintenance       bool   `json:"maintenance"`
	Lease             string `json:"lease"`
}

type workerResult struct {
	Up                   bool       `json:"up"`
	StatusCode           *int       `json:"statusCode"`
	ResponseMS           int        `json:"responseMs"`
	Message              *string    `json:"message"`
	CertificateExpiresAt *time.Time `json:"certificateExpiresAt"`
	Lease                string     `json:"lease"`
}

func (a *app) touchWorker(ctx context.Context, region, event string) error {
	claimed, result := event == "claim", event == "result"
	_, err := a.db.Exec(ctx, `INSERT INTO worker_heartbeats(region,started_at,last_seen_at,last_claimed_at,last_result_at,offline_after_seconds,claims,results)
		VALUES($1,now(),now(),CASE WHEN $2 THEN now() END,CASE WHEN $3 THEN now() END,$4,CASE WHEN $2 THEN 1 ELSE 0 END,CASE WHEN $3 THEN 1 ELSE 0 END)
		ON CONFLICT (region) DO UPDATE SET last_seen_at=now(),last_claimed_at=CASE WHEN $2 THEN now() ELSE worker_heartbeats.last_claimed_at END,last_result_at=CASE WHEN $3 THEN now() ELSE worker_heartbeats.last_result_at END,offline_after_seconds=$4,claims=worker_heartbeats.claims+CASE WHEN $2 THEN 1 ELSE 0 END,results=worker_heartbeats.results+CASE WHEN $3 THEN 1 ELSE 0 END`, region, claimed, result, int(workerOfflineAfter().Seconds()))
	return err
}

func validRegion(region string) bool {
	if len(region) < 1 || len(region) > 50 {
		return false
	}
	for _, character := range region {
		if !(character == '-' || character == '_' || character >= 'a' && character <= 'z' || character >= '0' && character <= '9') {
			return false
		}
	}
	return true
}

func (a *app) authorizeWorker(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected := []byte(strings.TrimSpace(env("PULSEOPS_WORKER_TOKEN", "")))
		provided := []byte(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if len(expected) < 32 || len(provided) != len(expected) || subtle.ConstantTimeCompare(provided, expected) != 1 {
			writeError(w, 401, "unauthorized worker")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *app) claimWorkerJob(w http.ResponseWriter, r *http.Request) {
	region := r.Header.Get("X-PulseOps-Region")
	if !validRegion(region) {
		writeError(w, 400, "invalid worker region")
		return
	}
	if err := a.touchWorker(r.Context(), region, "claim"); err != nil {
		writeError(w, 500, "database error")
		return
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		writeError(w, 500, "could not lease job")
		return
	}
	lease := "wl_" + hex.EncodeToString(secret)
	hash := sha256.Sum256([]byte(lease))
	var job workerJob
	err := a.db.QueryRow(r.Context(), `WITH candidate AS (
		SELECT m.id FROM monitors m LEFT JOIN worker_leases l ON l.monitor_id=m.id AND l.region=$2
		WHERE m.active AND m.monitor_type<>'heartbeat' AND COALESCE(l.next_check_at,'-infinity')<=now()
		AND (l.lease_until IS NULL OR l.lease_until<now()) ORDER BY COALESCE(l.next_check_at,'-infinity') FOR UPDATE OF m SKIP LOCKED LIMIT 1
	), leased AS (
		INSERT INTO worker_leases(monitor_id,region,next_check_at,lease_hash,lease_until)
		SELECT m.id,$2,now()+(m.interval_seconds*interval '1 second'),$1,now()+interval '2 minutes' FROM monitors m JOIN candidate c ON c.id=m.id
		ON CONFLICT (monitor_id,region) DO UPDATE SET next_check_at=EXCLUDED.next_check_at,lease_hash=EXCLUDED.lease_hash,lease_until=EXCLUDED.lease_until
		WHERE worker_leases.lease_until IS NULL OR worker_leases.lease_until<now() RETURNING monitor_id
	) SELECT m.id,m.name,m.url,m.monitor_type,m.expected_keyword,m.timeout_seconds,m.interval_seconds,m.failure_threshold,m.recovery_threshold,`+maintenanceActive+`
	FROM monitors m JOIN leased l ON l.monitor_id=m.id`, hash[:], region).Scan(&job.ID, &job.Name, &job.URL, &job.MonitorType, &job.ExpectedKeyword, &job.TimeoutSeconds, &job.IntervalSeconds, &job.FailureThreshold, &job.RecoveryThreshold, &job.Maintenance)
	if errors.Is(err, pgx.ErrNoRows) {
		w.WriteHeader(204)
		return
	}
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	job.Lease = lease
	writeJSON(w, 200, job)
}

func (a *app) submitWorkerResult(w http.ResponseWriter, r *http.Request) {
	region := r.Header.Get("X-PulseOps-Region")
	if !validRegion(region) {
		writeError(w, 400, "invalid worker region")
		return
	}
	id, err := monitorID(r)
	if err != nil {
		writeError(w, 400, "invalid monitor id")
		return
	}
	var result workerResult
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || result.ResponseMS < 0 || result.ResponseMS > 300000 || result.StatusCode != nil && (*result.StatusCode < 100 || *result.StatusCode > 599) || len(result.Lease) != 67 || !strings.HasPrefix(result.Lease, "wl_") || result.Message != nil && len(*result.Message) > 500 {
		writeError(w, 400, "invalid result")
		return
	}
	hash := sha256.Sum256([]byte(result.Lease))
	var item dueMonitor
	err = a.db.QueryRow(r.Context(), `WITH consumed AS (
		UPDATE worker_leases SET lease_hash=NULL,lease_until=NULL WHERE monitor_id=$1 AND region=$3 AND lease_hash=$2 AND lease_until>now() RETURNING monitor_id
	) SELECT m.id,m.name,m.url,m.timeout_seconds,m.interval_seconds,m.failure_threshold,m.recovery_threshold,`+maintenanceActive+`,m.monitor_type,m.expected_keyword
	FROM monitors m JOIN consumed c ON c.monitor_id=m.id`, id, hash[:], region).Scan(&item.id, &item.name, &item.url, &item.timeoutSeconds, &item.intervalSeconds, &item.failureThreshold, &item.recoveryThreshold, &item.maintenance, &item.monitorType, &item.expectedKeyword)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 409, "lease expired or already used")
		return
	}
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	item.region = region
	if err := a.recordCheck(r.Context(), item, checkResult{up: result.Up, statusCode: result.StatusCode, responseMS: result.ResponseMS, message: result.Message, certificateExpiresAt: result.CertificateExpiresAt}); err != nil {
		writeError(w, 500, "database error")
		return
	}
	if err := a.touchWorker(r.Context(), region, "result"); err != nil {
		writeError(w, 500, "database error")
		return
	}
	a.broadcast()
	w.WriteHeader(204)
}

func runWorker(ctx context.Context, coordinator, token, region string) error {
	if len(token) < 32 || !validRegion(region) {
		return errors.New("PULSEOPS_WORKER_TOKEN (32+ characters) and a valid PULSEOPS_WORKER_REGION are required")
	}
	client := &http.Client{Timeout: 45 * time.Second, Transport: traceHTTPTransport()}
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(coordinator, "/")+"/api/worker/claim", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-PulseOps-Region", region)
		response, err := client.Do(req)
		if err == nil && response.StatusCode == 204 {
			response.Body.Close()
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(2 * time.Second):
			}
			continue
		}
		if err != nil {
			log.Printf("worker claim: %v", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(5 * time.Second):
			}
			continue
		}
		if response.StatusCode != 200 {
			status := response.StatusCode
			response.Body.Close()
			if status == http.StatusTooManyRequests || status >= 500 {
				log.Printf("worker claim status %d; retrying", status)
				if !waitWorkerRetry(ctx, 5*time.Second) {
					return nil
				}
				continue
			}
			return fmt.Errorf("worker claim status %d", status)
		}
		var job workerJob
		err = json.NewDecoder(response.Body).Decode(&job)
		response.Body.Close()
		if err != nil {
			return err
		}
		item := dueMonitor{id: job.ID, name: job.Name, url: job.URL, monitorType: job.MonitorType, expectedKeyword: job.ExpectedKeyword, timeoutSeconds: job.TimeoutSeconds, intervalSeconds: job.IntervalSeconds, failureThreshold: job.FailureThreshold, recoveryThreshold: job.RecoveryThreshold, maintenance: job.Maintenance}
		jobContext, span := otel.Tracer("pulseops/worker").Start(ctx, "worker.job")
		span.SetAttributes(attribute.Int64("monitor.id", job.ID), attribute.String("worker.region", region), attribute.String("monitor.type", job.MonitorType))
		item.region = region
		checked := performCheck(jobContext, item)
		payload, _ := json.Marshal(workerResult{Up: checked.up, StatusCode: checked.statusCode, ResponseMS: checked.responseMS, Message: checked.message, CertificateExpiresAt: checked.certificateExpiresAt, Lease: job.Lease})
		err = submitWorkerResult(jobContext, client, strings.TrimRight(coordinator, "/"), token, region, job.ID, payload, 5*time.Second)
		span.End()
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			log.Printf("worker result abandoned: %v", err)
		}
	}
}

func waitWorkerRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func submitWorkerResult(ctx context.Context, client *http.Client, coordinator, token, region string, id int64, payload []byte, retryDelay time.Duration) error {
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/api/worker/results/%d", coordinator, id), bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-PulseOps-Region", region)
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		if err != nil {
			log.Printf("worker result: %v; retrying", err)
			if !waitWorkerRetry(ctx, retryDelay) {
				return ctx.Err()
			}
			continue
		}
		status := response.StatusCode
		response.Body.Close()
		if status == http.StatusNoContent {
			return nil
		}
		if status == http.StatusTooManyRequests || status >= 500 {
			log.Printf("worker result status %d; retrying", status)
			if !waitWorkerRetry(ctx, retryDelay) {
				return ctx.Err()
			}
			continue
		}
		return fmt.Errorf("worker result status %d", status)
	}
}
