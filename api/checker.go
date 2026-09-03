package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type dueMonitor struct {
	id                                  int64
	name, url                           string
	timeoutSeconds                      int
	failureThreshold, recoveryThreshold int
	maintenance                         bool
}
type checkResult struct {
	up                   bool
	statusCode           *int
	responseMS           int
	message              *string
	certificateExpiresAt *time.Time
}

func (a *app) runScheduler(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	semaphore := make(chan struct{}, 10)
	lastNotifications := time.Time{}
	lastCleanup := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if time.Since(lastCleanup) >= 24*time.Hour {
				lastCleanup = time.Now()
				go func() {
					if _, err := a.db.Exec(ctx, `DELETE FROM checks WHERE checked_at < now()-interval '90 days'`); err != nil {
						log.Printf("retention cleanup: %v", err)
					}
				}()
			}
			if time.Since(lastNotifications) >= 5*time.Second {
				lastNotifications = time.Now()
				go a.deliverNotifications(ctx)
			}
			items, err := a.claimDue(ctx)
			if err != nil {
				log.Printf("claim checks: %v", err)
				continue
			}
			for _, item := range items {
				item := item
				semaphore <- struct{}{}
				go func() {
					defer func() { <-semaphore }()
					result := performCheck(ctx, item)
					if err := a.recordCheck(ctx, item, result); err != nil {
						log.Printf("record check %d: %v", item.id, err)
						return
					}
					a.broadcast()
				}()
			}
		}
	}
}

func (a *app) claimDue(ctx context.Context) ([]dueMonitor, error) {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	rows, err := tx.Query(ctx, `SELECT id,name,url,timeout_seconds,failure_threshold,recovery_threshold,maintenance_until IS NOT NULL AND maintenance_until>now() FROM monitors WHERE active AND next_check_at<=now() ORDER BY next_check_at FOR UPDATE SKIP LOCKED LIMIT 20`)
	if err != nil {
		return nil, err
	}
	items := []dueMonitor{}
	for rows.Next() {
		var item dueMonitor
		if err := rows.Scan(&item.id, &item.name, &item.url, &item.timeoutSeconds, &item.failureThreshold, &item.recoveryThreshold, &item.maintenance); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, item)
	}
	rows.Close()
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	for _, item := range items {
		if _, err := tx.Exec(ctx, `UPDATE monitors SET next_check_at=now()+(interval_seconds*interval '1 second') WHERE id=$1`, item.id); err != nil {
			return nil, err
		}
	}
	return items, tx.Commit(ctx)
}

func safeDialer() func(context.Context, string, string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, errors.New("hostname has no address")
		}
		for _, resolved := range ips {
			ip := resolved.IP
			if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
				return nil, errors.New("private or local target addresses are not allowed")
			}
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}
}

func performCheck(parent context.Context, item dueMonitor) checkResult {
	ctx, cancel := context.WithTimeout(parent, time.Duration(item.timeoutSeconds)*time.Second)
	defer cancel()
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: safeDialer(), TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: time.Duration(item.timeoutSeconds) * time.Second, DisableKeepAlives: true}
	client := http.Client{Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, item.url, nil)
	if err != nil {
		return failedResult(0, err)
	}
	req.Header.Set("User-Agent", "PulseOps/1.0")
	started := time.Now()
	response, err := client.Do(req)
	elapsed := int(time.Since(started).Milliseconds())
	if err != nil {
		return failedResult(elapsed, err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	status := response.StatusCode
	result := checkResult{up: status >= 200 && status < 400, statusCode: &status, responseMS: elapsed}
	if !result.up {
		message := fmt.Sprintf("HTTP %d", status)
		result.message = &message
	}
	if response.TLS != nil && len(response.TLS.PeerCertificates) > 0 {
		expiry := response.TLS.PeerCertificates[0].NotAfter.UTC()
		result.certificateExpiresAt = &expiry
	}
	return result
}
func failedResult(ms int, err error) checkResult {
	message := err.Error()
	if len(message) > 500 {
		message = message[:500]
	}
	return checkResult{responseMS: ms, message: &message}
}

func (a *app) recordCheck(ctx context.Context, item dueMonitor, result checkResult) error {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	_, err = tx.Exec(ctx, `INSERT INTO checks(monitor_id,up,status_code,response_ms,error,certificate_expires_at) VALUES($1,$2,$3,$4,$5,$6)`, item.id, result.up, result.statusCode, result.responseMS, result.message, result.certificateExpiresAt)
	if err != nil {
		return err
	}
	var failures, successes int
	err = tx.QueryRow(ctx, `UPDATE monitors SET last_checked_at=now(),last_status_code=$2,last_response_ms=$3,last_error=$4,certificate_expires_at=$5,consecutive_failures=CASE WHEN $6 OR $7 THEN 0 ELSE consecutive_failures+1 END,consecutive_successes=CASE WHEN NOT $6 OR $7 THEN 0 ELSE consecutive_successes+1 END,updated_at=now() WHERE id=$1 RETURNING consecutive_failures,consecutive_successes`, item.id, result.statusCode, result.responseMS, result.message, result.certificateExpiresAt, result.up, item.maintenance).Scan(&failures, &successes)
	if err != nil {
		return err
	}
	eventKey, subject, body := "", "", ""
	openIncident, resolveIncident := transition(result.up, item.maintenance, failures, successes, item.failureThreshold, item.recoveryThreshold)
	if openIncident {
		var incidentID int64
		err = tx.QueryRow(ctx, `INSERT INTO incidents(monitor_id,cause) VALUES($1,$2) ON CONFLICT (monitor_id) WHERE resolved_at IS NULL DO NOTHING RETURNING id`, item.id, value(result.message, "check failed")).Scan(&incidentID)
		if err == nil {
			eventKey = "incident-open:" + strconv.FormatInt(incidentID, 10)
			subject = "PulseOps incident: " + item.name
			body = item.name + " is down. " + value(result.message, "Check failed")
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	} else if resolveIncident {
		var incidentID int64
		err = tx.QueryRow(ctx, `UPDATE incidents SET resolved_at=now() WHERE monitor_id=$1 AND resolved_at IS NULL RETURNING id`, item.id).Scan(&incidentID)
		if err == nil {
			eventKey = "incident-resolved:" + strconv.FormatInt(incidentID, 10)
			subject = "PulseOps recovered: " + item.name
			body = item.name + " is responding again."
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	if !item.maintenance && eventKey == "" && result.certificateExpiresAt != nil {
		days, _ := strconv.Atoi(env("SSL_WARNING_DAYS", "14"))
		if time.Until(*result.certificateExpiresAt) < time.Duration(days)*24*time.Hour {
			eventKey = "ssl:" + result.certificateExpiresAt.Format("2006-01-02")
			subject = "PulseOps SSL warning: " + item.name
			body = fmt.Sprintf("%s certificate expires on %s.", item.name, result.certificateExpiresAt.Format("2006-01-02"))
		}
	}
	if eventKey != "" {
		for _, channel := range configuredChannels() {
			if _, err := tx.Exec(ctx, `INSERT INTO notification_deliveries(monitor_id,event_key,subject,body,channel) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, item.id, eventKey, subject, body, channel); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}
func transition(up, maintenance bool, failures, successes, failureThreshold, recoveryThreshold int) (bool, bool) {
	if maintenance {
		return false, false
	}
	return !up && failures >= failureThreshold, up && successes >= recoveryThreshold
}
func value(input *string, fallback string) string {
	if input != nil {
		return *input
	}
	return fallback
}

func (a *app) deliverNotifications(ctx context.Context) {
	channels := configuredChannels()
	if len(channels) == 0 {
		return
	}
	rows, err := a.db.Query(ctx, `WITH due AS (SELECT id FROM notification_deliveries WHERE delivered_at IS NULL AND next_attempt_at<=now() AND channel=ANY($1) ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 10) UPDATE notification_deliveries n SET attempts=n.attempts+1,next_attempt_at=now()+interval '5 minutes' FROM due WHERE n.id=due.id RETURNING n.id,n.subject,n.body,n.attempts,n.channel`, channels)
	if err != nil {
		log.Printf("notification queue: %v", err)
		return
	}
	type pending struct {
		id            int64
		subject, body string
		attempts      int
		channel       string
	}
	items := []pending{}
	for rows.Next() {
		var item pending
		if rows.Scan(&item.id, &item.subject, &item.body, &item.attempts, &item.channel) == nil {
			items = append(items, item)
		}
	}
	rows.Close()
	for _, item := range items {
		if err := sendNotification(item.channel, item.subject, item.body); err == nil {
			_, _ = a.db.Exec(ctx, `UPDATE notification_deliveries SET delivered_at=now() WHERE id=$1`, item.id)
		} else {
			delay := 1 << min(item.attempts, 8)
			_, _ = a.db.Exec(ctx, `UPDATE notification_deliveries SET next_attempt_at=now()+($2*interval '1 minute') WHERE id=$1`, item.id, delay)
			log.Printf("Brevo delivery %d: %v", item.id, err)
		}
	}
}

func configuredChannels() []string {
	channels := []string{}
	if os.Getenv("BREVO_API_KEY") != "" && os.Getenv("ALERT_EMAIL_TO") != "" && os.Getenv("ALERT_EMAIL_FROM") != "" {
		channels = append(channels, "email")
	}
	if os.Getenv("ALERT_WEBHOOK_URL") != "" {
		channels = append(channels, "webhook")
	}
	return channels
}

func sendNotification(channel, subject, body string) error {
	if channel == "email" {
		return sendBrevo(subject, body)
	}
	return sendWebhook(subject, body)
}

func sendWebhook(subject, body string) error {
	target := os.Getenv("ALERT_WEBHOOK_URL")
	payload, _ := json.Marshal(map[string]string{"event": "pulseops.alert", "subject": subject, "body": body})
	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "PulseOps/1.0")
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("status %d", response.StatusCode)
	}
	return nil
}

func sendBrevo(subject, body string) error {
	key, to, from := os.Getenv("BREVO_API_KEY"), os.Getenv("ALERT_EMAIL_TO"), os.Getenv("ALERT_EMAIL_FROM")
	if key == "" || to == "" || from == "" {
		return errors.New("Brevo is not configured")
	}
	payload := map[string]any{"sender": map[string]string{"email": from, "name": "PulseOps"}, "to": []map[string]string{{"email": to}}, "subject": subject, "textContent": body}
	encoded, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, "https://api.brevo.com/v3/smtp/email", bytes.NewReader(encoded))
	req.Header.Set("api-key", key)
	req.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return fmt.Errorf("status %d: %s", response.StatusCode, strings.TrimSpace(string(message)))
	}
	return nil
}
