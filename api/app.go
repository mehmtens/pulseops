package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type app struct {
	db        *pgxpool.Pool
	token     string
	clientsMu sync.Mutex
	clients   map[chan struct{}]struct{}
}
type monitor struct {
	ID                   int64      `json:"id"`
	Name                 string     `json:"name"`
	URL                  string     `json:"url"`
	IntervalSeconds      int        `json:"intervalSeconds"`
	TimeoutSeconds       int        `json:"timeoutSeconds"`
	Active               bool       `json:"active"`
	Public               bool       `json:"public"`
	FailureThreshold     int        `json:"failureThreshold"`
	RecoveryThreshold    int        `json:"recoveryThreshold"`
	ConsecutiveFailures  int        `json:"consecutiveFailures"`
	ConsecutiveSuccesses int        `json:"consecutiveSuccesses"`
	MaintenanceUntil     *time.Time `json:"maintenanceUntil"`
	LastCheckedAt        *time.Time `json:"lastCheckedAt"`
	LastStatusCode       *int       `json:"lastStatusCode"`
	LastResponseMS       *int       `json:"lastResponseMs"`
	LastError            *string    `json:"lastError"`
	CertificateExpiresAt *time.Time `json:"certificateExpiresAt"`
	Uptime24h            float64    `json:"uptime24h"`
	IncidentStartedAt    *time.Time `json:"incidentStartedAt"`
	MonitorType          string     `json:"monitorType"`
	ExpectedKeyword      string     `json:"expectedKeyword"`
}
type monitorInput struct {
	Name              string     `json:"name"`
	URL               string     `json:"url"`
	IntervalSeconds   int        `json:"intervalSeconds"`
	TimeoutSeconds    int        `json:"timeoutSeconds"`
	Active            *bool      `json:"active"`
	Public            *bool      `json:"public"`
	FailureThreshold  int        `json:"failureThreshold"`
	RecoveryThreshold int        `json:"recoveryThreshold"`
	MaintenanceUntil  *time.Time `json:"maintenanceUntil"`
	MonitorType       string     `json:"monitorType"`
	ExpectedKeyword   string     `json:"expectedKeyword"`
}

func newApp(db *pgxpool.Pool, token string) *app {
	return &app{db: db, token: token, clients: make(map[chan struct{}]struct{})}
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /api/readyz", a.ready)
	mux.HandleFunc("GET /api/status", a.publicStatus)
	mux.HandleFunc("GET /api/events", a.events)
	mux.HandleFunc("POST /api/heartbeat/{token}", a.heartbeat)
	mux.HandleFunc("GET /api/openapi.json", a.openAPI)
	mux.Handle("GET /api/monitors", a.authorize(http.HandlerFunc(a.listMonitors)))
	mux.Handle("POST /api/monitors", a.authorizeWrite(http.HandlerFunc(a.createMonitor)))
	mux.Handle("PUT /api/monitors/{id}", a.authorizeWrite(http.HandlerFunc(a.updateMonitor)))
	mux.Handle("DELETE /api/monitors/{id}", a.authorizeWrite(http.HandlerFunc(a.deleteMonitor)))
	mux.Handle("GET /api/monitors/{id}/checks", a.authorize(http.HandlerFunc(a.listChecks)))
	mux.Handle("GET /api/incidents", a.authorize(http.HandlerFunc(a.listIncidents)))
	mux.Handle("PATCH /api/incidents/{id}", a.authorizeWrite(http.HandlerFunc(a.updateIncident)))
	mux.Handle("GET /api/reports/uptime", a.authorize(http.HandlerFunc(a.uptimeReport)))
	mux.Handle("GET /api/keys", a.authorizeAdmin(http.HandlerFunc(a.listKeys)))
	mux.Handle("POST /api/keys", a.authorizeAdmin(http.HandlerFunc(a.createKey)))
	mux.Handle("DELETE /api/keys/{id}", a.authorizeAdmin(http.HandlerFunc(a.deleteKey)))
	mux.Handle("GET /api/organization", a.authorize(http.HandlerFunc(a.getOrganization)))
	mux.Handle("PUT /api/organization", a.authorizeAdmin(http.HandlerFunc(a.updateOrganization)))
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
func (a *app) authorize(next http.Handler) http.Handler {
	return a.authorizeScope(false, next)
}
func (a *app) authorizeWrite(next http.Handler) http.Handler {
	return a.authorizeScope(true, next)
}
func roleAllows(scope string, write, admin bool) bool {
	if admin {
		return scope == "admin"
	}
	return !write || scope == "write" || scope == "admin"
}
func (a *app) authorizeAdmin(next http.Handler) http.Handler {
	return a.authorizeRole(false, true, next)
}
func (a *app) authorizeScope(write bool, next http.Handler) http.Handler {
	return a.authorizeRole(write, false, next)
}
func (a *app) authorizeRole(write, admin bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if len(provided) == len(a.token) && subtle.ConstantTimeCompare([]byte(provided), []byte(a.token)) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		if a.db == nil || !strings.HasPrefix(provided, "po_") {
			writeError(w, 401, "unauthorized")
			return
		}
		hash := sha256.Sum256([]byte(provided))
		var scope string
		if err := a.db.QueryRow(r.Context(), `UPDATE api_keys SET last_used_at=now() WHERE token_hash=$1 RETURNING scope`, hash[:]).Scan(&scope); err != nil {
			writeError(w, 401, "unauthorized")
			return
		}
		if !roleAllows(scope, write, admin) {
			writeError(w, 403, "insufficient role")
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (a *app) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if a.db.Ping(ctx) != nil {
		writeJSON(w, 503, map[string]string{"status": "unavailable"})
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ready"})
}

const monitorSelect = `SELECT m.id,m.name,m.url,m.interval_seconds,m.timeout_seconds,m.active,m.public,m.failure_threshold,m.recovery_threshold,m.consecutive_failures,m.consecutive_successes,m.maintenance_until,m.last_checked_at,m.last_status_code,m.last_response_ms,m.last_error,m.certificate_expires_at,COALESCE((SELECT 100.0*count(*) FILTER (WHERE up)/NULLIF(count(*),0) FROM checks c WHERE c.monitor_id=m.id AND c.checked_at>now()-interval '24 hours'),100),(SELECT started_at FROM incidents i WHERE i.monitor_id=m.id AND i.resolved_at IS NULL),m.monitor_type,m.expected_keyword FROM monitors m`

func scanMonitor(row pgx.Row) (monitor, error) {
	var m monitor
	err := row.Scan(&m.ID, &m.Name, &m.URL, &m.IntervalSeconds, &m.TimeoutSeconds, &m.Active, &m.Public, &m.FailureThreshold, &m.RecoveryThreshold, &m.ConsecutiveFailures, &m.ConsecutiveSuccesses, &m.MaintenanceUntil, &m.LastCheckedAt, &m.LastStatusCode, &m.LastResponseMS, &m.LastError, &m.CertificateExpiresAt, &m.Uptime24h, &m.IncidentStartedAt, &m.MonitorType, &m.ExpectedKeyword)
	return m, err
}
func (a *app) queryMonitors(ctx context.Context, publicOnly bool) ([]monitor, error) {
	query := monitorSelect
	if publicOnly {
		query += " WHERE m.public"
	}
	query += " ORDER BY m.name"
	rows, err := a.db.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []monitor{}
	for rows.Next() {
		m, err := scanMonitor(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, m)
	}
	return items, rows.Err()
}
func (a *app) listMonitors(w http.ResponseWriter, r *http.Request) {
	items, err := a.queryMonitors(r.Context(), false)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	writeJSON(w, 200, items)
}
func (a *app) publicStatus(w http.ResponseWriter, r *http.Request) {
	items, err := a.queryMonitors(r.Context(), true)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	rows, err := a.db.Query(r.Context(), `SELECT i.id,m.name,i.started_at,i.resolved_at,i.cause FROM incidents i JOIN monitors m ON m.id=i.monitor_id WHERE m.public ORDER BY i.started_at DESC LIMIT 20`)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	defer rows.Close()
	incidents := []map[string]any{}
	for rows.Next() {
		var id int64
		var name, cause string
		var started time.Time
		var resolved *time.Time
		if rows.Scan(&id, &name, &started, &resolved, &cause) != nil {
			writeError(w, 500, "database error")
			return
		}
		incidents = append(incidents, map[string]any{"id": id, "monitorName": name, "startedAt": started, "resolvedAt": resolved, "cause": cause})
	}
	if rows.Err() != nil {
		writeError(w, 500, "database error")
		return
	}
	writeJSON(w, 200, map[string]any{"updatedAt": time.Now().UTC(), "monitors": items, "incidents": incidents, "page": map[string]string{"name": env("STATUS_PAGE_NAME", "PulseOps"), "message": env("STATUS_PAGE_MESSAGE", "Live service health and incident updates.")}})
}

func decodeInput(w http.ResponseWriter, r *http.Request) (monitorInput, error) {
	var in monitorInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		return in, errors.New("invalid JSON")
	}
	in.Name, in.URL = strings.TrimSpace(in.Name), strings.TrimSpace(in.URL)
	in.ExpectedKeyword = strings.TrimSpace(in.ExpectedKeyword)
	if in.MonitorType == "" {
		in.MonitorType = "http"
	}
	if len(in.Name) < 1 || len(in.Name) > 100 || len(in.URL) > 2048 || len(in.ExpectedKeyword) > 500 {
		return in, errors.New("name or URL has an invalid length")
	}
	if in.MonitorType != "http" && in.MonitorType != "heartbeat" && in.MonitorType != "tcp" && in.MonitorType != "dns" {
		return in, errors.New("monitor type must be http, heartbeat, tcp, or dns")
	}
	if in.MonitorType == "http" {
		parsed, err := url.ParseRequestURI(in.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
			return in, errors.New("URL must be an absolute HTTP or HTTPS URL without credentials")
		}
	} else if in.MonitorType == "heartbeat" {
		in.URL, in.ExpectedKeyword = "", ""
	} else {
		in.ExpectedKeyword = ""
		if strings.ContainsAny(in.URL, "/?#@ ") {
			return in, errors.New("network target is invalid")
		}
		if in.MonitorType == "tcp" {
			host, port, err := net.SplitHostPort(in.URL)
			number, numberErr := strconv.Atoi(port)
			if err != nil || host == "" || numberErr != nil || number < 1 || number > 65535 {
				return in, errors.New("TCP target must be host:port")
			}
		}
		if in.MonitorType == "dns" && (in.URL == "" || net.ParseIP(in.URL) != nil) {
			return in, errors.New("DNS target must be a hostname")
		}
	}
	if in.IntervalSeconds == 0 {
		in.IntervalSeconds = 60
	}
	if in.TimeoutSeconds == 0 {
		in.TimeoutSeconds = 10
	}
	if in.FailureThreshold == 0 {
		in.FailureThreshold = 2
	}
	if in.RecoveryThreshold == 0 {
		in.RecoveryThreshold = 2
	}
	if in.IntervalSeconds < 15 || in.IntervalSeconds > 86400 || in.TimeoutSeconds < 1 || in.TimeoutSeconds > 30 {
		return in, errors.New("interval must be 15-86400 seconds and timeout 1-30 seconds")
	}
	if in.FailureThreshold < 1 || in.FailureThreshold > 10 || in.RecoveryThreshold < 1 || in.RecoveryThreshold > 10 {
		return in, errors.New("failure and recovery thresholds must be 1-10")
	}
	if in.MaintenanceUntil != nil && in.MaintenanceUntil.After(time.Now().Add(366*24*time.Hour)) {
		return in, errors.New("maintenance cannot be scheduled more than one year ahead")
	}
	return in, nil
}
func defaults(in monitorInput) (bool, bool) {
	active, public := true, true
	if in.Active != nil {
		active = *in.Active
	}
	if in.Public != nil {
		public = *in.Public
	}
	return active, public
}
func (a *app) createMonitor(w http.ResponseWriter, r *http.Request) {
	in, err := decodeInput(w, r)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	active, public := defaults(in)
	var id int64
	var heartbeatToken string
	var heartbeatHash []byte
	if in.MonitorType == "heartbeat" {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			writeError(w, 500, "could not create heartbeat")
			return
		}
		heartbeatToken = "hb_" + hex.EncodeToString(secret)
		sum := sha256.Sum256([]byte(heartbeatToken))
		heartbeatHash = sum[:]
	}
	if err := a.db.QueryRow(r.Context(), `INSERT INTO monitors(name,url,interval_seconds,timeout_seconds,active,public,failure_threshold,recovery_threshold,maintenance_until,monitor_type,expected_keyword,heartbeat_token_hash,next_check_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,CASE WHEN $10='heartbeat' THEN now()+($3::integer*interval '1 second') ELSE now() END) RETURNING id`, in.Name, in.URL, in.IntervalSeconds, in.TimeoutSeconds, active, public, in.FailureThreshold, in.RecoveryThreshold, in.MaintenanceUntil, in.MonitorType, in.ExpectedKeyword, heartbeatHash).Scan(&id); err != nil {
		writeError(w, 500, "database error")
		return
	}
	m, err := scanMonitor(a.db.QueryRow(r.Context(), monitorSelect+" WHERE m.id=$1", id))
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	if heartbeatToken == "" {
		writeJSON(w, 201, m)
		return
	}
	writeJSON(w, 201, map[string]any{"monitor": m, "heartbeatToken": heartbeatToken, "heartbeatUrl": "/api/heartbeat/" + heartbeatToken})
}
func monitorID(r *http.Request) (int64, error) { return strconv.ParseInt(r.PathValue("id"), 10, 64) }
func (a *app) updateMonitor(w http.ResponseWriter, r *http.Request) {
	id, err := monitorID(r)
	if err != nil {
		writeError(w, 400, "invalid monitor id")
		return
	}
	in, err := decodeInput(w, r)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	active, public := defaults(in)
	command, err := a.db.Exec(r.Context(), `UPDATE monitors SET name=$2,url=$3,interval_seconds=$4,timeout_seconds=$5,active=$6,public=$7,failure_threshold=$8,recovery_threshold=$9,maintenance_until=$10,expected_keyword=$11,next_check_at=LEAST(next_check_at,now()),updated_at=now() WHERE id=$1 AND monitor_type=$12`, id, in.Name, in.URL, in.IntervalSeconds, in.TimeoutSeconds, active, public, in.FailureThreshold, in.RecoveryThreshold, in.MaintenanceUntil, in.ExpectedKeyword, in.MonitorType)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	if command.RowsAffected() == 0 {
		writeError(w, 404, "monitor not found")
		return
	}
	m, err := scanMonitor(a.db.QueryRow(r.Context(), monitorSelect+" WHERE m.id=$1", id))
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	writeJSON(w, 200, m)
}
func (a *app) deleteMonitor(w http.ResponseWriter, r *http.Request) {
	id, err := monitorID(r)
	if err != nil {
		writeError(w, 400, "invalid monitor id")
		return
	}
	command, err := a.db.Exec(r.Context(), "DELETE FROM monitors WHERE id=$1", id)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	if command.RowsAffected() == 0 {
		writeError(w, 404, "monitor not found")
		return
	}
	w.WriteHeader(204)
}
func (a *app) listChecks(w http.ResponseWriter, r *http.Request) {
	id, err := monitorID(r)
	if err != nil {
		writeError(w, 400, "invalid monitor id")
		return
	}
	rows, err := a.db.Query(r.Context(), `SELECT checked_at,up,status_code,response_ms,error,certificate_expires_at FROM checks WHERE monitor_id=$1 ORDER BY checked_at DESC LIMIT 100`, id)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var at time.Time
		var up bool
		var status, ms *int
		var message *string
		var cert *time.Time
		if rows.Scan(&at, &up, &status, &ms, &message, &cert) != nil {
			writeError(w, 500, "database error")
			return
		}
		items = append(items, map[string]any{"checkedAt": at, "up": up, "statusCode": status, "responseMs": ms, "error": message, "certificateExpiresAt": cert})
	}
	if rows.Err() != nil {
		writeError(w, 500, "database error")
		return
	}
	writeJSON(w, 200, items)
}

func (a *app) listIncidents(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(r.Context(), `SELECT i.id,i.monitor_id,m.name,i.started_at,i.resolved_at,i.cause,i.acknowledged_at,i.note FROM incidents i JOIN monitors m ON m.id=i.monitor_id ORDER BY i.started_at DESC LIMIT 100`)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, monitorID int64
		var name, cause string
		var started time.Time
		var resolved, acknowledged *time.Time
		var note string
		if err := rows.Scan(&id, &monitorID, &name, &started, &resolved, &cause, &acknowledged, &note); err != nil {
			writeError(w, 500, "database error")
			return
		}
		items = append(items, map[string]any{"id": id, "monitorId": monitorID, "monitorName": name, "startedAt": started, "resolvedAt": resolved, "cause": cause, "acknowledgedAt": acknowledged, "note": note})
	}
	if rows.Err() != nil {
		writeError(w, 500, "database error")
		return
	}
	writeJSON(w, 200, items)
}

func (a *app) updateIncident(w http.ResponseWriter, r *http.Request) {
	id, err := monitorID(r)
	if err != nil {
		writeError(w, 400, "invalid incident id")
		return
	}
	var in struct {
		Acknowledged bool   `json:"acknowledged"`
		Note         string `json:"note"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	in.Note = strings.TrimSpace(in.Note)
	if len(in.Note) > 1000 {
		writeError(w, 400, "note must be at most 1000 characters")
		return
	}
	command, err := a.db.Exec(r.Context(), `UPDATE incidents SET acknowledged_at=CASE WHEN $2 THEN COALESCE(acknowledged_at,now()) ELSE NULL END,note=$3 WHERE id=$1`, id, in.Acknowledged, in.Note)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	if command.RowsAffected() == 0 {
		writeError(w, 404, "incident not found")
		return
	}
	w.WriteHeader(204)
}

func (a *app) uptimeReport(w http.ResponseWriter, r *http.Request) {
	days := 30
	if raw := r.URL.Query().Get("days"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 90 {
			writeError(w, 400, "days must be 1-90")
			return
		}
		days = parsed
	}
	format := r.URL.Query().Get("format")
	if format != "" && format != "json" && format != "csv" {
		writeError(w, 400, "format must be json or csv")
		return
	}
	rows, err := a.db.Query(r.Context(), `SELECT m.id,m.name,COALESCE(100.0*count(c.id) FILTER (WHERE c.up)/NULLIF(count(c.id),0),100),COALESCE(avg(c.response_ms),0)::integer,count(c.id) FROM monitors m LEFT JOIN checks c ON c.monitor_id=m.id AND c.checked_at>now()-($1*interval '1 day') GROUP BY m.id,m.name ORDER BY m.name`, days)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	defer rows.Close()
	type reportRow struct {
		MonitorID         int64   `json:"monitorId"`
		MonitorName       string  `json:"monitorName"`
		Uptime            float64 `json:"uptime"`
		AverageResponseMS int64   `json:"averageResponseMs"`
		Checks            int64   `json:"checks"`
	}
	items := []reportRow{}
	for rows.Next() {
		var item reportRow
		if rows.Scan(&item.MonitorID, &item.MonitorName, &item.Uptime, &item.AverageResponseMS, &item.Checks) != nil {
			writeError(w, 500, "database error")
			return
		}
		items = append(items, item)
	}
	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="pulseops-uptime.csv"`)
		writer := csv.NewWriter(w)
		_ = writer.Write([]string{"monitor", "uptime_percent", "average_response_ms", "checks"})
		for _, item := range items {
			_ = writer.Write([]string{item.MonitorName, strconv.FormatFloat(item.Uptime, 'f', 2, 64), strconv.FormatInt(item.AverageResponseMS, 10), strconv.FormatInt(item.Checks, 10)})
		}
		writer.Flush()
		return
	}
	writeJSON(w, 200, map[string]any{"days": days, "generatedAt": time.Now().UTC(), "monitors": items})
}

func (a *app) listKeys(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(r.Context(), `SELECT id,name,token_prefix,scope,last_used_at,created_at FROM api_keys ORDER BY created_at DESC`)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id int64
		var name, prefix, scope string
		var used *time.Time
		var created time.Time
		if rows.Scan(&id, &name, &prefix, &scope, &used, &created) != nil {
			writeError(w, 500, "database error")
			return
		}
		items = append(items, map[string]any{"id": id, "name": name, "prefix": prefix, "scope": scope, "lastUsedAt": used, "createdAt": created})
	}
	writeJSON(w, 200, items)
}
func (a *app) createKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name  string `json:"name"`
		Scope string `json:"scope"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if len(in.Name) < 1 || len(in.Name) > 100 || (in.Scope != "read" && in.Scope != "write" && in.Scope != "admin") {
		writeError(w, 400, "name and scope are invalid")
		return
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		writeError(w, 500, "could not create key")
		return
	}
	token := "po_" + hex.EncodeToString(secret)
	hash := sha256.Sum256([]byte(token))
	prefix := token[:11]
	var id int64
	if err := a.db.QueryRow(r.Context(), `INSERT INTO api_keys(name,token_hash,token_prefix,scope,organization_id) VALUES($1,$2,$3,$4,(SELECT id FROM organizations ORDER BY id LIMIT 1)) RETURNING id`, in.Name, hash[:], prefix, in.Scope).Scan(&id); err != nil {
		writeError(w, 500, "database error")
		return
	}
	writeJSON(w, 201, map[string]any{"id": id, "name": in.Name, "scope": in.Scope, "prefix": prefix, "token": token})
}

func (a *app) getOrganization(w http.ResponseWriter, r *http.Request) {
	var id int64
	var name string
	var created time.Time
	if err := a.db.QueryRow(r.Context(), `SELECT id,name,created_at FROM organizations ORDER BY id LIMIT 1`).Scan(&id, &name, &created); err != nil {
		writeError(w, 500, "database error")
		return
	}
	writeJSON(w, 200, map[string]any{"id": id, "name": name, "createdAt": created})
}

func (a *app) updateOrganization(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if len(in.Name) < 1 || len(in.Name) > 100 {
		writeError(w, 400, "name is invalid")
		return
	}
	var id int64
	if err := a.db.QueryRow(r.Context(), `UPDATE organizations SET name=$1 WHERE id=(SELECT id FROM organizations ORDER BY id LIMIT 1) RETURNING id`, in.Name).Scan(&id); err != nil {
		writeError(w, 500, "database error")
		return
	}
	writeJSON(w, 200, map[string]any{"id": id, "name": in.Name})
}
func (a *app) deleteKey(w http.ResponseWriter, r *http.Request) {
	id, err := monitorID(r)
	if err != nil {
		writeError(w, 400, "invalid key id")
		return
	}
	command, err := a.db.Exec(r.Context(), `DELETE FROM api_keys WHERE id=$1`, id)
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	if command.RowsAffected() == 0 {
		writeError(w, 404, "key not found")
		return
	}
	w.WriteHeader(204)
}

func (a *app) heartbeat(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if len(token) != 67 || !strings.HasPrefix(token, "hb_") {
		writeError(w, 404, "heartbeat not found")
		return
	}
	hash := sha256.Sum256([]byte(token))
	var item dueMonitor
	err := a.db.QueryRow(r.Context(), `UPDATE monitors SET next_check_at=now()+(interval_seconds*interval '1 second') WHERE heartbeat_token_hash=$1 AND monitor_type='heartbeat' AND active RETURNING id,name,url,timeout_seconds,failure_threshold,recovery_threshold,maintenance_until IS NOT NULL AND maintenance_until>now(),monitor_type,expected_keyword`, hash[:]).Scan(&item.id, &item.name, &item.url, &item.timeoutSeconds, &item.failureThreshold, &item.recoveryThreshold, &item.maintenance, &item.monitorType, &item.expectedKeyword)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "heartbeat not found")
		return
	}
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	if err := a.recordCheck(r.Context(), item, checkResult{up: true}); err != nil {
		writeError(w, 500, "database error")
		return
	}
	a.broadcast()
	w.WriteHeader(204)
}

func (a *app) openAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write(openAPIDocument) //nolint:errcheck
}

func (a *app) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	client := make(chan struct{}, 1)
	a.clientsMu.Lock()
	a.clients[client] = struct{}{}
	a.clientsMu.Unlock()
	defer func() { a.clientsMu.Lock(); delete(a.clients, client); a.clientsMu.Unlock() }()
	fmt.Fprint(w, "event: connected\ndata: {}\n\n")
	flusher.Flush()
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-client:
			fmt.Fprint(w, "event: status\ndata: {}\n\n")
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}
func (a *app) broadcast() {
	a.clientsMu.Lock()
	defer a.clientsMu.Unlock()
	for client := range a.clients {
		select {
		case client <- struct{}{}:
		default:
		}
	}
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
