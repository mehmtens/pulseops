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
	"slices"
	"strconv"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

type dueMonitor struct {
	id                                  int64
	name, url                           string
	monitorType, expectedKeyword        string
	timeoutSeconds                      int
	intervalSeconds                     int
	failureThreshold, recoveryThreshold int
	maintenance                         bool
	region                              string
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
					if _, err := a.db.Exec(ctx, `DELETE FROM audit_events WHERE created_at < now()-($1*interval '1 day')`, auditRetentionDays()); err != nil {
						log.Printf("audit retention cleanup: %v", err)
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

func auditRetentionDays() int {
	days, err := strconv.Atoi(env("AUDIT_RETENTION_DAYS", "365"))
	if err != nil || days < 30 || days > 3650 {
		return 365
	}
	return days
}

func (a *app) claimDue(ctx context.Context) ([]dueMonitor, error) {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	rows, err := tx.Query(ctx, `SELECT m.id,m.name,m.url,m.timeout_seconds,m.interval_seconds,m.failure_threshold,m.recovery_threshold,`+maintenanceActive+`,m.monitor_type,m.expected_keyword FROM monitors m WHERE m.active AND m.next_check_at<=now() ORDER BY m.next_check_at FOR UPDATE OF m SKIP LOCKED LIMIT 20`)
	if err != nil {
		return nil, err
	}
	items := []dueMonitor{}
	for rows.Next() {
		var item dueMonitor
		if err := rows.Scan(&item.id, &item.name, &item.url, &item.timeoutSeconds, &item.intervalSeconds, &item.failureThreshold, &item.recoveryThreshold, &item.maintenance, &item.monitorType, &item.expectedKeyword); err != nil {
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
	ctx, span := otel.Tracer("pulseops/checker").Start(parent, "monitor.check")
	span.SetAttributes(attribute.Int64("monitor.id", item.id), attribute.String("monitor.type", item.monitorType), attribute.String("worker.region", item.region))
	defer span.End()
	if item.monitorType == "heartbeat" {
		return failedResult(0, errors.New("heartbeat overdue"))
	}
	if item.monitorType == "tcp" || item.monitorType == "dns" {
		return performNetworkCheck(ctx, item)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(item.timeoutSeconds)*time.Second)
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
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	status := response.StatusCode
	result := checkResult{up: status >= 200 && status < 400, statusCode: &status, responseMS: elapsed}
	if !result.up {
		message := fmt.Sprintf("HTTP %d", status)
		result.message = &message
	}
	if result.up && !contentMatches(body, item.expectedKeyword) {
		message := "expected content not found"
		result.up, result.message = false, &message
	}
	if response.TLS != nil && len(response.TLS.PeerCertificates) > 0 {
		expiry := response.TLS.PeerCertificates[0].NotAfter.UTC()
		result.certificateExpiresAt = &expiry
	}
	return result
}
func performNetworkCheck(parent context.Context, item dueMonitor) checkResult {
	ctx, cancel := context.WithTimeout(parent, time.Duration(item.timeoutSeconds)*time.Second)
	defer cancel()
	started := time.Now()
	if item.monitorType == "dns" {
		addresses, err := net.DefaultResolver.LookupHost(ctx, item.url)
		if err != nil {
			return failedResult(int(time.Since(started).Milliseconds()), err)
		}
		if len(addresses) == 0 {
			return failedResult(int(time.Since(started).Milliseconds()), errors.New("hostname has no address"))
		}
		return checkResult{up: true, responseMS: int(time.Since(started).Milliseconds())}
	}
	connection, err := safeDialer()(ctx, "tcp", item.url)
	if err != nil {
		return failedResult(int(time.Since(started).Milliseconds()), err)
	}
	connection.Close()
	return checkResult{up: true, responseMS: int(time.Since(started).Milliseconds())}
}
func contentMatches(body []byte, expected string) bool {
	return expected == "" || bytes.Contains(body, []byte(expected))
}
func failedResult(ms int, err error) checkResult {
	message := err.Error()
	if len(message) > 500 {
		message = message[:500]
	}
	return checkResult{responseMS: ms, message: &message}
}

func (a *app) recordCheck(ctx context.Context, item dueMonitor, result checkResult) error {
	ctx, span := otel.Tracer("pulseops/checker").Start(ctx, "check.record")
	span.SetAttributes(attribute.Int64("monitor.id", item.id), attribute.String("worker.region", item.region), attribute.Bool("check.up", result.up))
	defer span.End()
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var maintenance bool
	var notificationChannels []string
	var escalationDelay int
	if err := tx.QueryRow(ctx, `SELECT `+maintenanceActive+`,m.notification_channels,m.escalation_delay_seconds FROM monitors m WHERE m.id=$1 FOR UPDATE`, item.id).Scan(&maintenance, &notificationChannels, &escalationDelay); err != nil {
		return err
	}
	region := item.region
	if region == "" {
		region = "local"
	}
	_, err = tx.Exec(ctx, `INSERT INTO checks(monitor_id,up,status_code,response_ms,error,certificate_expires_at,region) VALUES($1,$2,$3,$4,$5,$6,$7)`, item.id, result.up, result.statusCode, result.responseMS, result.message, result.certificateExpiresAt, region)
	if err != nil {
		return err
	}
	quorum := regionQuorum()
	if item.region != "" && item.region != "local" && quorum > 1 {
		var upVotes, downVotes int
		err = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE up),count(*) FILTER (WHERE NOT up) FROM (SELECT DISTINCT ON (region) up FROM checks WHERE monitor_id=$1 AND region<>'local' AND checked_at>now()-($2*interval '2 seconds') ORDER BY region,checked_at DESC) votes`, item.id, item.intervalSeconds).Scan(&upVotes, &downVotes)
		if err != nil {
			return err
		}
		decision, decided := quorumDecision(upVotes, downVotes, quorum)
		if !decided {
			return tx.Commit(ctx)
		}
		result.up = decision
		if !decision {
			message := fmt.Sprintf("regional quorum failure (%d regions)", downVotes)
			result.message = &message
		}
	}
	var failures, successes int
	remoteQuorum := item.region != "" && item.region != "local" && quorum > 1
	err = tx.QueryRow(ctx, `UPDATE monitors SET last_checked_at=now(),last_status_code=$2,last_response_ms=$3,last_error=$4,certificate_expires_at=$5,consecutive_failures=CASE WHEN $6 OR $7 THEN 0 ELSE consecutive_failures+1 END,consecutive_successes=CASE WHEN NOT $6 OR $7 THEN 0 ELSE consecutive_successes+1 END,last_quorum_at=CASE WHEN $8 THEN now() ELSE last_quorum_at END,updated_at=now() WHERE id=$1 AND (NOT $8 OR last_quorum_at IS NULL OR last_quorum_at<now()-(interval_seconds*interval '0.5 seconds')) RETURNING consecutive_failures,consecutive_successes`, item.id, result.statusCode, result.responseMS, result.message, result.certificateExpiresAt, result.up, maintenance, remoteQuorum).Scan(&failures, &successes)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	eventKey, subject, body := "", "", ""
	openIncident, resolveIncident := transition(result.up, maintenance, failures, successes, item.failureThreshold, item.recoveryThreshold)
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
	if !maintenance && eventKey == "" && result.certificateExpiresAt != nil {
		days, _ := strconv.Atoi(env("SSL_WARNING_DAYS", "14"))
		if time.Until(*result.certificateExpiresAt) < time.Duration(days)*24*time.Hour {
			eventKey = "ssl:" + result.certificateExpiresAt.Format("2006-01-02")
			subject = "PulseOps SSL warning: " + item.name
			body = fmt.Sprintf("%s certificate expires on %s.", item.name, result.certificateExpiresAt.Format("2006-01-02"))
		}
	}
	if eventKey != "" {
		delay := 0
		if strings.HasPrefix(eventKey, "incident-open:") {
			delay = escalationDelay
		}
		available := configuredChannels()
		for _, channel := range notificationChannels {
			if !slices.Contains(available, channel) {
				continue
			}
			if channel == "push" {
				if _, err := tx.Exec(ctx, `INSERT INTO notification_deliveries(monitor_id,event_key,subject,body,channel,web_push_subscription_id,next_attempt_at) SELECT $1,$2,$3,$4,'push',sm.subscription_id,now()+($5*interval '1 second') FROM web_push_subscription_monitors sm WHERE sm.monitor_id=$1 ON CONFLICT DO NOTHING`, item.id, eventKey, subject, body, delay); err != nil {
					return err
				}
				continue
			}
			if _, err := tx.Exec(ctx, `INSERT INTO notification_deliveries(monitor_id,event_key,subject,body,channel,next_attempt_at) VALUES($1,$2,$3,$4,$5,now()+($6*interval '1 second')) ON CONFLICT DO NOTHING`, item.id, eventKey, subject, body, channel, delay); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}
func quorumDecision(up, down, required int) (bool, bool) {
	if up >= required {
		return true, true
	}
	if down >= required {
		return false, true
	}
	return false, false
}
func regionQuorum() int {
	quorum, err := strconv.Atoi(env("PULSEOPS_REGION_QUORUM", "1"))
	if err != nil || quorum < 1 || quorum > 10 {
		return 1
	}
	return quorum
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
	rows, err := a.db.Query(ctx, `WITH due AS (SELECT id FROM notification_deliveries WHERE delivered_at IS NULL AND next_attempt_at<=now() AND channel=ANY($1) ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 10) UPDATE notification_deliveries n SET attempts=n.attempts+1,next_attempt_at=now()+interval '5 minutes' FROM due WHERE n.id=due.id RETURNING n.id,n.subject,n.body,n.attempts,n.channel,(SELECT endpoint FROM web_push_subscriptions WHERE id=n.web_push_subscription_id),(SELECT p256dh FROM web_push_subscriptions WHERE id=n.web_push_subscription_id),(SELECT auth FROM web_push_subscriptions WHERE id=n.web_push_subscription_id)`, channels)
	if err != nil {
		log.Printf("notification queue: %v", err)
		return
	}
	type pending struct {
		id                     int64
		subject, body          string
		attempts               int
		channel                string
		endpoint, p256dh, auth *string
	}
	items := []pending{}
	for rows.Next() {
		var item pending
		if rows.Scan(&item.id, &item.subject, &item.body, &item.attempts, &item.channel, &item.endpoint, &item.p256dh, &item.auth) == nil {
			items = append(items, item)
		}
	}
	rows.Close()
	for _, item := range items {
		if err := sendNotification(item.channel, item.subject, item.body, item.endpoint, item.p256dh, item.auth); err == nil {
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
	if os.Getenv("WEB_PUSH_VAPID_PUBLIC_KEY") != "" && os.Getenv("WEB_PUSH_VAPID_PRIVATE_KEY") != "" && os.Getenv("WEB_PUSH_SUBJECT") != "" {
		channels = append(channels, "push")
	}
	return channels
}

func sendNotification(channel, subject, body string, endpoint, p256dh, auth *string) error {
	if channel == "email" {
		return sendBrevo(subject, body)
	}
	if channel == "push" {
		if endpoint == nil || p256dh == nil || auth == nil {
			return errors.New("push subscription is missing")
		}
		return sendWebPush(subject, body, *endpoint, *p256dh, *auth)
	}
	return sendWebhook(subject, body)
}

func sendWebPush(subject, body, endpoint, p256dh, auth string) error {
	payload, _ := json.Marshal(map[string]string{"title": subject, "body": body, "url": "/"})
	client := webPushClient()
	response, err := webpush.SendNotification(payload, &webpush.Subscription{Endpoint: endpoint, Keys: webpush.Keys{P256dh: p256dh, Auth: auth}}, &webpush.Options{HTTPClient: client, Subscriber: os.Getenv("WEB_PUSH_SUBJECT"), VAPIDPublicKey: os.Getenv("WEB_PUSH_VAPID_PUBLIC_KEY"), VAPIDPrivateKey: os.Getenv("WEB_PUSH_VAPID_PRIVATE_KEY"), TTL: 300})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("status %d", response.StatusCode)
	}
	return nil
}

func webPushClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: safeDialer(), TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second}}
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
