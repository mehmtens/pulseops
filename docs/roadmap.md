# PulseOps roadmap

## Completed MVP

- [x] Go API, React + TypeScript, PostgreSQL, Caddy, Docker Compose, CI, and health/readiness checks.
- [x] Validated monitor CRUD with protected administrative endpoints.
- [x] PostgreSQL-backed scheduling and multi-instance-safe due-check claims.
- [x] Response history, 24-hour uptime, and automatic incident open/resolve behavior.
- [x] SSL certificate expiry tracking and warning events.
- [x] Durable, idempotent Brevo notification outbox with retry backoff.
- [x] Server-Sent Events dashboard refresh and public status page.
- [x] Embedded, locked migrations; SSRF controls; security headers; graceful shutdown; retention; backup/restore guidance.
- [x] Flapping thresholds, maintenance windows, generic webhook alerts, and channel-isolated delivery leases.
- [x] Scoped API keys, incident acknowledgement/notes, 1–90 day uptime reports, and public incident history.
- [x] Cron heartbeats, HTTP content assertions, and an embedded OpenAPI 3.1 contract.

## Verification criteria met

1. Backend unit tests pass.
2. Frontend test and production TypeScript build pass.
3. Fresh Docker Compose stack reaches healthy PostgreSQL and ready API.
4. Real HTTPS monitor records HTTP status, response time, uptime, and certificate expiry.
5. A failing response opens an incident and a healthy response resolves it.
6. Unauthorized administration returns HTTP 401; public status contains only public monitors.

## Post-MVP options

Only add these when a real deployment needs them:

- Multi-user accounts, organizations, and role-based access.
- First-party notification adapters such as Slack, Telegram, or PagerDuty when generic webhooks are insufficient.
- Regional checker workers and geographic consensus.
- Recurring maintenance schedules.
- Long-term rollups beyond the built-in 90-day raw-check retention when raw-query performance becomes measurable.
- OpenTelemetry export and deployment-specific dashboards.
