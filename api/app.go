package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
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
	LastCheckedAt        *time.Time `json:"lastCheckedAt"`
	LastStatusCode       *int       `json:"lastStatusCode"`
	LastResponseMS       *int       `json:"lastResponseMs"`
	LastError            *string    `json:"lastError"`
	CertificateExpiresAt *time.Time `json:"certificateExpiresAt"`
	Uptime24h            float64    `json:"uptime24h"`
	IncidentStartedAt    *time.Time `json:"incidentStartedAt"`
}
type monitorInput struct {
	Name            string `json:"name"`
	URL             string `json:"url"`
	IntervalSeconds int    `json:"intervalSeconds"`
	TimeoutSeconds  int    `json:"timeoutSeconds"`
	Active          *bool  `json:"active"`
	Public          *bool  `json:"public"`
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
	mux.Handle("GET /api/monitors", a.authorize(http.HandlerFunc(a.listMonitors)))
	mux.Handle("POST /api/monitors", a.authorize(http.HandlerFunc(a.createMonitor)))
	mux.Handle("PUT /api/monitors/{id}", a.authorize(http.HandlerFunc(a.updateMonitor)))
	mux.Handle("DELETE /api/monitors/{id}", a.authorize(http.HandlerFunc(a.deleteMonitor)))
	mux.Handle("GET /api/monitors/{id}/checks", a.authorize(http.HandlerFunc(a.listChecks)))
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if len(provided) != len(a.token) || subtle.ConstantTimeCompare([]byte(provided), []byte(a.token)) != 1 {
			writeError(w, 401, "unauthorized")
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

const monitorSelect = `SELECT m.id,m.name,m.url,m.interval_seconds,m.timeout_seconds,m.active,m.public,m.last_checked_at,m.last_status_code,m.last_response_ms,m.last_error,m.certificate_expires_at,COALESCE((SELECT 100.0*count(*) FILTER (WHERE up)/NULLIF(count(*),0) FROM checks c WHERE c.monitor_id=m.id AND c.checked_at>now()-interval '24 hours'),100),(SELECT started_at FROM incidents i WHERE i.monitor_id=m.id AND i.resolved_at IS NULL) FROM monitors m`

func scanMonitor(row pgx.Row) (monitor, error) {
	var m monitor
	err := row.Scan(&m.ID, &m.Name, &m.URL, &m.IntervalSeconds, &m.TimeoutSeconds, &m.Active, &m.Public, &m.LastCheckedAt, &m.LastStatusCode, &m.LastResponseMS, &m.LastError, &m.CertificateExpiresAt, &m.Uptime24h, &m.IncidentStartedAt)
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
	writeJSON(w, 200, map[string]any{"updatedAt": time.Now().UTC(), "monitors": items})
}

func decodeInput(w http.ResponseWriter, r *http.Request) (monitorInput, error) {
	var in monitorInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		return in, errors.New("invalid JSON")
	}
	in.Name, in.URL = strings.TrimSpace(in.Name), strings.TrimSpace(in.URL)
	if len(in.Name) < 1 || len(in.Name) > 100 || len(in.URL) > 2048 {
		return in, errors.New("name or URL has an invalid length")
	}
	parsed, err := url.ParseRequestURI(in.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
		return in, errors.New("URL must be an absolute HTTP or HTTPS URL without credentials")
	}
	if in.IntervalSeconds == 0 {
		in.IntervalSeconds = 60
	}
	if in.TimeoutSeconds == 0 {
		in.TimeoutSeconds = 10
	}
	if in.IntervalSeconds < 15 || in.IntervalSeconds > 86400 || in.TimeoutSeconds < 1 || in.TimeoutSeconds > 30 {
		return in, errors.New("interval must be 15-86400 seconds and timeout 1-30 seconds")
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
	if err := a.db.QueryRow(r.Context(), `INSERT INTO monitors(name,url,interval_seconds,timeout_seconds,active,public) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`, in.Name, in.URL, in.IntervalSeconds, in.TimeoutSeconds, active, public).Scan(&id); err != nil {
		writeError(w, 500, "database error")
		return
	}
	m, err := scanMonitor(a.db.QueryRow(r.Context(), monitorSelect+" WHERE m.id=$1", id))
	if err != nil {
		writeError(w, 500, "database error")
		return
	}
	writeJSON(w, 201, m)
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
	command, err := a.db.Exec(r.Context(), `UPDATE monitors SET name=$2,url=$3,interval_seconds=$4,timeout_seconds=$5,active=$6,public=$7,next_check_at=LEAST(next_check_at,now()),updated_at=now() WHERE id=$1`, id, in.Name, in.URL, in.IntervalSeconds, in.TimeoutSeconds, active, public)
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
	writeJSON(w, 200, items)
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
