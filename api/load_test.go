package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestCoordinatorWorkerLoadProfile is intentionally opt-in. It exercises the real
// HTTP claim/result path and PostgreSQL leases without contacting monitored URLs.
// Example: PULSEOPS_RUN_LOAD_TEST=1 PULSEOPS_LOAD_MONITORS=900 go test -run LoadProfile -v
func TestCoordinatorWorkerLoadProfile(t *testing.T) {
	if os.Getenv("PULSEOPS_RUN_LOAD_TEST") != "1" {
		t.Skip("set PULSEOPS_RUN_LOAD_TEST=1")
	}
	pool := integrationPool(t)
	monitors := loadInt("PULSEOPS_LOAD_MONITORS", 900)
	regions := loadInt("PULSEOPS_LOAD_REGIONS", 3)
	clientsPerRegion := loadInt("PULSEOPS_LOAD_CLIENTS_PER_REGION", 4)
	if monitors < 1 || monitors > 100000 || regions < 1 || regions > 10 || clientsPerRegion < 1 || clientsPerRegion > 50 {
		t.Fatal("invalid load profile")
	}
	// Existing monitors would be claimed by the profile and corrupt the result.
	// Refuse to mutate a populated database; callers must supply a fresh,
	// expendable database for every repeatable run.
	var existingMonitors, existingChecks int64
	if err := pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM monitors),(SELECT count(*) FROM checks)`).Scan(&existingMonitors, &existingChecks); err != nil {
		t.Fatal(err)
	}
	if existingMonitors != 0 || existingChecks != 0 {
		t.Fatalf("load profile requires an empty expendable database (found %d monitors and %d checks)", existingMonitors, existingChecks)
	}
	_, err := pool.Exec(t.Context(), `INSERT INTO monitors(name,url,monitor_type,interval_seconds,timeout_seconds,failure_threshold,recovery_threshold,notification_channels)
		SELECT 'load-'||n,'https://example.com/'||n,'http',15,1,1,1,ARRAY[]::text[] FROM generate_series(1,$1) n`, monitors)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("w", 32)
	t.Setenv("PULSEOPS_WORKER_TOKEN", token)
	t.Setenv("PULSEOPS_REGION_QUORUM", "1")
	server := httptest.NewServer(newApp(pool, strings.Repeat("a", 16)).routes())
	defer server.Close()
	target := int64(monitors * regions)
	var completed, failures atomic.Int64
	regionalJobs := make([]chan struct{}, regions)
	for regionIndex := range regionalJobs {
		regionalJobs[regionIndex] = make(chan struct{}, monitors)
		for range monitors {
			regionalJobs[regionIndex] <- struct{}{}
		}
		close(regionalJobs[regionIndex])
	}
	latencies := make(chan time.Duration, target*2)
	started := time.Now()
	deadline := started.Add(90 * time.Second)
	var wg sync.WaitGroup
	for regionIndex := 0; regionIndex < regions; regionIndex++ {
		region := fmt.Sprintf("load-%02d", regionIndex+1)
		for clientIndex := 0; clientIndex < clientsPerRegion; clientIndex++ {
			wg.Add(1)
			go func(regionIndex int) {
				defer wg.Done()
				client := &http.Client{Timeout: 10 * time.Second}
				for range regionalJobs[regionIndex] {
				claimAttempt:
					for time.Now().Before(deadline) {
						before := time.Now()
						req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/worker/claim", nil)
						req.Header.Set("Authorization", "Bearer "+token)
						req.Header.Set("X-PulseOps-Region", region)
						response, requestErr := client.Do(req)
						select {
						case latencies <- time.Since(before):
						default:
						}
						if requestErr != nil {
							failures.Add(1)
							break claimAttempt
						}
						if response.StatusCode == http.StatusNoContent {
							response.Body.Close()
							time.Sleep(5 * time.Millisecond)
							continue
						}
						var job workerJob
						decodeErr := json.NewDecoder(response.Body).Decode(&job)
						response.Body.Close()
						if response.StatusCode != http.StatusOK || decodeErr != nil {
							failures.Add(1)
							break claimAttempt
						}
						payload, _ := json.Marshal(workerResult{Up: true, ResponseMS: 10, Lease: job.Lease})
						before = time.Now()
						req, _ = http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/worker/results/%d", server.URL, job.ID), bytes.NewReader(payload))
						req.Header.Set("Authorization", "Bearer "+token)
						req.Header.Set("X-PulseOps-Region", region)
						req.Header.Set("Content-Type", "application/json")
						response, requestErr = client.Do(req)
						select {
						case latencies <- time.Since(before):
						default:
						}
						if requestErr == nil && response.StatusCode == http.StatusNoContent {
							completed.Add(1)
						} else {
							failures.Add(1)
						}
						if response != nil {
							response.Body.Close()
						}
						break claimAttempt
					}
				}
			}(regionIndex)
		}
	}
	wg.Wait()
	close(latencies)
	elapsed := time.Since(started)
	values := make([]time.Duration, 0, len(latencies))
	for value := range latencies {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	p95 := time.Duration(0)
	if len(values) > 0 {
		p95 = values[(len(values)-1)*95/100]
	}
	rawStarted := time.Now()
	var rawChecks int64
	var uptime float64
	err = pool.QueryRow(t.Context(), `SELECT count(*),COALESCE(100.0*count(*) FILTER (WHERE up)/NULLIF(count(*),0),100) FROM checks WHERE checked_at>=now()-interval '30 days'`).Scan(&rawChecks, &uptime)
	rawQuery := time.Since(rawStarted)
	result := map[string]any{"monitors": monitors, "regions": regions, "clientsPerRegion": clientsPerRegion, "checks": completed.Load(), "failures": failures.Load(), "elapsedSeconds": elapsed.Seconds(), "checksPerSecond": float64(completed.Load()) / elapsed.Seconds(), "httpP95Ms": float64(p95.Microseconds()) / 1000, "raw30DayQueryMs": float64(rawQuery.Microseconds()) / 1000, "rawRows": rawChecks, "uptime": uptime}
	encoded, _ := json.Marshal(result)
	t.Log(string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if completed.Load() != target || failures.Load() != 0 {
		t.Fatalf("incomplete load profile: %s", encoded)
	}
}

func loadInt(name string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(name))
	if err != nil || value == 0 {
		return fallback
	}
	return value
}
