<div align="center">

# PulseOps

### Know before your users do.

A focused, self-hosted uptime monitor and incident communication platform.

[![CI](https://github.com/mehmtens/pulseops/actions/workflows/ci.yml/badge.svg)](https://github.com/mehmtens/pulseops/actions/workflows/ci.yml)

[Quick start](#quick-start) · [Features](#what-you-get) · [Configuration](#configuration) · [Operations](#operations)

</div>

---

PulseOps watches HTTP and HTTPS endpoints and cron heartbeats, records response time and uptime, opens and resolves incidents automatically, warns before certificates expire, sends alerts, and publishes a live status page. It stays intentionally small: one Go service, one PostgreSQL database, one React app, and Caddy at the edge.

## What you get

- **Reliable endpoint checks** — configurable 15-second to 24-hour intervals and hard request timeouts.
- **Cron heartbeats** — a private, one-time URL acts as a dead man's switch for scheduled jobs.
- **Network checks** — verify public TCP ports and DNS resolution with the same incident lifecycle.
- **Content assertions** — optionally require expected text within the first 64 KiB of a successful HTTP response.
- **Incident lifecycle** — a failure opens one incident; recovery resolves it transactionally and keeps a dashboard timeline.
- **Flapping control** — configurable consecutive failure and recovery thresholds prevent noisy one-off alerts.
- **Maintenance windows** — continue collecting measurements while suppressing incident transitions and notifications.
- **Useful history** — response time, HTTP status, errors, 24-hour uptime, and 90-day check retention.
- **SSL awareness** — certificate expiry capture and configurable early warning.
- **Durable notifications** — per-monitor email, webhook, and optional Web Push policies with escalation delay, idempotency, and retry backoff in PostgreSQL.
- **Generic webhooks** — send the same durable incident and SSL events to Slack-compatible relays or your own automation.
- **Live dashboard** — monitor CRUD, operational metrics, and Server-Sent Events updates.
- **Public status page** — only monitors explicitly marked public are exposed at `/status`.
- **Operational follow-through** — acknowledge incidents, attach operator notes, and publish recent public incident history.
- **30-day reporting** — inspect per-monitor uptime, average response time, and check volume for any 1–90 day window.
- **Team roles** — expiring one-time invitations create independently revocable viewer, operator, or admin access; secrets are stored as SHA-256 hashes and shown once.
- **Status communication** — group public monitors into components and publish operator-authored incident updates.
- **Regional workers** — run the same small binary near users; workers pull leased checks over HTTPS and report region-tagged results.
- **Audit trail** — admins can inspect successful monitor, incident, key, and organization mutations with actor, path, status, and timestamp.
- **Production-minded defaults** — SSRF protection, bearer-token administration, security headers, non-root API image, graceful shutdown, database migrations, and health/readiness probes.
- **OpenAPI contract** — the machine-readable API description is served at `/api/openapi.json`.

## Architecture

```text
Browser ──HTTP/S──▶ Caddy ─────▶ React dashboard / public status
                       │
                       └─ /api ─▶ Go API + scheduler ──▶ PostgreSQL
                                           │
                                           └───────────▶ Brevo
```

PostgreSQL owns schedules, leases, checks, incidents, and notification state. `FOR UPDATE SKIP LOCKED` makes due-check claiming safe across multiple API instances without Redis. Migrations are embedded in the API binary and applied under a PostgreSQL advisory lock.

## Quick start

Requirements: Docker and Docker Compose.

```bash
git clone https://github.com/mehmtens/pulseops.git
cd pulseops
cp .env.example .env
docker compose up --build -d
```

Before starting, edit `.env` and replace these local defaults:

```dotenv
POSTGRES_PASSWORD=use-a-long-random-password
DATABASE_URL=postgres://pulseops:use-a-long-random-password@postgres:5432/pulseops?sslmode=disable
PULSEOPS_API_TOKEN=use-another-long-random-secret
```

Open [http://localhost:3000](http://localhost:3000), enter `PULSEOPS_API_TOKEN`, then add an HTTP or HTTPS endpoint. The public page is [http://localhost:3000/status](http://localhost:3000/status).

```bash
# Service state
docker compose ps

# API liveness and database readiness
curl http://localhost:3000/api/healthz
curl http://localhost:3000/api/readyz

# Stop without deleting PostgreSQL data
docker compose down
```

## Configuration

| Variable | Required | Default | Purpose |
|---|---:|---|---|
| `POSTGRES_DB` | No | `pulseops` | Database name |
| `POSTGRES_USER` | No | `pulseops` | Database user |
| `POSTGRES_PASSWORD` | Production | local value | Database password |
| `DATABASE_URL` | Production | local Compose URL | API connection string |
| `PULSEOPS_API_TOKEN` | Yes | local development token | Dashboard/API bearer token; minimum 16 characters |
| `WEB_PORT` | No | `3000` | Host HTTP port |
| `WEB_HTTPS_PORT` | No | `3443` | Host HTTPS port |
| `PULSEOPS_SITE_ADDRESS` | No | `:80` | Caddy address; set a real domain for automatic HTTPS |
| `BREVO_API_KEY` | For email | empty | Brevo v3 API key |
| `ALERT_EMAIL_TO` | For email | empty | Alert recipient |
| `ALERT_EMAIL_FROM` | For email | empty | Verified Brevo sender |
| `ALERT_WEBHOOK_URL` | For webhooks | empty | HTTP endpoint receiving PulseOps alert JSON |
| `WEB_PUSH_VAPID_PUBLIC_KEY` | For Web Push | empty | URL-safe base64 VAPID public key |
| `WEB_PUSH_VAPID_PRIVATE_KEY` | For Web Push | empty | VAPID private key; keep secret |
| `WEB_PUSH_SUBJECT` | For Web Push | empty | VAPID contact, usually `mailto:ops@example.com` |
| `SSL_WARNING_DAYS` | No | `14` | Certificate warning threshold |
| `STATUS_PAGE_NAME` | No | `PulseOps` | Public status page name |
| `STATUS_PAGE_MESSAGE` | No | service-health message | Public status page message |
| `PULSEOPS_WORKER_TOKEN` | For workers | empty | Shared 32+ character secret for regional workers |
| `PULSEOPS_WORKER_REGION` | For workers | `local` | Lowercase worker region label |
| `PULSEOPS_COORDINATOR_URL` | For workers | empty | Coordinator URL; setting it runs this image as a worker |
| `PULSEOPS_LOCAL_CHECKS` | No | `true` | Set `false` on a coordinator using only remote workers |
| `PULSEOPS_REGION_QUORUM` | No | `1` | Distinct regional votes required before health changes |
| `PULSEOPS_EXPECTED_REGIONS` | No | empty | Comma-separated worker regions that must remain online |
| `PULSEOPS_WORKER_OFFLINE_AFTER_SECONDS` | No | `30` | Time without a claim/result before a region is warned offline (5–3600) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | No | empty | OTLP/HTTP collector endpoint; enables coordinator and worker traces |
| `GRAFANA_ADMIN_PASSWORD` | Observability profile | local value | Grafana administrator password |
| `GRAFANA_DB_PASSWORD` | Observability profile | local value | Password for the automatically provisioned read-only dashboard role |
| `AUDIT_RETENTION_DAYS` | No | `365` | Successful audit events retained (30–3650 days) |

For a public host, point DNS at the server and use ports 80/443:

```dotenv
WEB_PORT=80
WEB_HTTPS_PORT=443
PULSEOPS_SITE_ADDRESS=status.example.com
```

Caddy will request and renew certificates automatically. Keep the generated Caddy volumes and PostgreSQL volume persistent.

## API

Administrative endpoints require `Authorization: Bearer <PULSEOPS_API_TOKEN>`.

| Method | Path | Access | Purpose |
|---|---|---|---|
| `GET` | `/api/healthz` | Public | Process liveness |
| `GET` | `/api/readyz` | Public | PostgreSQL readiness |
| `GET` | `/api/status` | Public | Public monitor status |
| `GET` | `/api/events` | Public | Status-only SSE notifications |
| `POST` | `/api/heartbeat/{token}` | Private URL | Record a successful cron/job heartbeat |
| `GET` | `/api/openapi.json` | Public | OpenAPI 3.1 description |
| `GET/POST` | `/api/monitors` | Read/Write | List/create monitors |
| `PUT/DELETE` | `/api/monitors/{id}` | Write | Update/delete a monitor |
| `GET` | `/api/maintenance-schedules` | Member | List recurring maintenance schedules in one request |
| `GET/POST` | `/api/monitors/{id}/maintenance-schedules` | Read/Write | List/create weekly maintenance windows with timezone-aware exception dates |
| `DELETE` | `/api/monitors/{id}/maintenance-schedules/{scheduleId}` | Write | Delete a recurring maintenance window |
| `GET` | `/api/monitors/{id}/checks` | Read | Latest 100 checks |
| `GET` | `/api/incidents` | Read | Latest 100 incidents |
| `PATCH` | `/api/incidents/{id}` | Write | Acknowledge an incident and save an operator note |
| `POST` | `/api/incidents/{id}/updates` | Write | Publish a status-page incident update |
| `GET` | `/api/reports/uptime?days=30` | Read | Per-monitor uptime report for 1–90 days; add `format=csv` to download |
| `GET/POST` | `/api/keys` | Admin | List/create team access keys |
| `DELETE` | `/api/keys/{id}` | Admin | Revoke an access key |
| `GET/POST` | `/api/invitations` | Admin | List/create expiring one-time invitations |
| `DELETE` | `/api/invitations/{id}` | Admin | Revoke an unused invitation |
| `POST` | `/api/invitations/accept` | Public | Exchange a valid invitation for revocable member access |
| `GET` | `/api/push/vapid-public-key` | Member | Read the configured Web Push application key |
| `PUT/DELETE` | `/api/push/subscription` | Member | Save/remove this member's monitor-scoped browser subscription |
| `GET/PUT` | `/api/organization` | Member/Admin | Read or rename the organization |
| `GET` | `/api/audit` | Admin | Recent successful changes; add `format=csv` to export |
| `GET` | `/api/notifications/metrics` | Member | Notification queue health |
| `GET` | `/api/workers` | Member | Worker heartbeats, counters, online state, and region/quorum warnings |

For a heartbeat monitor, send a request after the job succeeds:

```bash
curl -X POST https://status.example.com/api/heartbeat/hb_your_one_time_secret
```

The heartbeat URL is displayed only when the monitor is created. Store it like a password. If it is lost, create a replacement monitor.

## Security model

PulseOps supports a team organization with three enforced roles: viewers can inspect operations, operators can also manage monitors and incidents, and admins can manage team access and the organization profile. Invitation links expire after 1–720 hours and can be accepted once; acceptance creates a separate key that admins can revoke without restarting the service. The server-configured root token remains the recovery credential. Access keys and invitation secrets are stored only as hashes. Public APIs expose only public monitor health and operator-published incident updates, never credentials, private notes, authors, or notification settings. Endpoint validation rejects credentials in URLs, private/loopback/link-local targets, DNS resolutions to private networks, redirects beyond five hops, and TLS below 1.2.

Put internet-facing installations behind HTTPS, use unique secrets, restrict host access, and keep Docker/PostgreSQL patched. Access is token-based; browser password login, SSO, and cross-organization tenant isolation are not part of this deployment model.

### Regional workers

Set one shared worker secret on the coordinator and disable its local checks when all checks should run remotely:

```env
PULSEOPS_WORKER_TOKEN=a-separate-random-secret-at-least-32-characters
PULSEOPS_LOCAL_CHECKS=false
PULSEOPS_REGION_QUORUM=2
PULSEOPS_EXPECTED_REGIONS=eu-west,us-east
```

Run the regular API image in any region without a database connection:

```bash
docker run --rm \
  -e PULSEOPS_COORDINATOR_URL=https://status.example.com \
  -e PULSEOPS_WORKER_TOKEN=a-separate-random-secret-at-least-32-characters \
  -e PULSEOPS_WORKER_REGION=eu-west \
  pulseops-api
```

Add worker containers with distinct lowercase region names. Each region schedules every monitor independently. `PULSEOPS_REGION_QUORUM=2` requires two regions to agree before health changes; increase it only when at least that many regions are continuously online. PostgreSQL leasing prevents duplicate work inside a region, while one-time two-minute leases reject expired or replayed results. Always expose the coordinator over HTTPS.

Workers update their heartbeat on every accepted claim and result. The dashboard refreshes fleet state every 10 seconds and warns when an expected region is absent or online regions fall below quorum. With the defaults, region recovery is visible on the first successful claim (normally within the two-second idle poll). Transient coordinator 429/5xx responses and result transport failures are retried. If a process dies with a check in flight, only that monitor waits for its lease to expire; the worst-case re-claim bound is the two-minute lease plus the poll interval.

## Operations

### OpenTelemetry and example dashboard

Setting `OTEL_EXPORTER_OTLP_ENDPOINT` enables W3C trace propagation and batched OTLP/HTTP export for the coordinator API and remote worker HTTP calls. No exporter or background telemetry work is created when the variable is empty. Start the supplied local Tempo/Grafana example with:

```bash
docker compose -f compose.yaml -f deploy/observability/compose.yaml up -d
```

Grafana is available on `http://localhost:3001`. The provisioned **PulseOps operations** dashboard shows active monitors, check rate, online worker regions, expired leases, regional throughput, response-time p95, and the worker table. A one-shot initializer creates `pulseops_grafana` with schema usage and SELECT-only table privileges; Grafana never receives the application database owner credentials. The Tempo data source is provisioned for trace search. Change both `GRAFANA_ADMIN_PASSWORD` and `GRAFANA_DB_PASSWORD` outside local development.

### Measured capacity

The repeatable profile lives in `api/load_test.go` and uses the real HTTP claim/result endpoints, PostgreSQL leases, check recording, and the raw 30-day aggregation query. It never contacts monitor targets. Run it against an expendable PostgreSQL database:

```bash
cd api
PULSEOPS_RUN_LOAD_TEST=1 \
PULSEOPS_LOAD_MONITORS=900 \
PULSEOPS_LOAD_REGIONS=3 \
PULSEOPS_LOAD_CLIENTS_PER_REGION=4 \
TEST_DATABASE_URL='postgres://pulseops:secret@localhost:5432/pulseops_test?sslmode=disable' \
go test -run TestCoordinatorWorkerLoadProfile -count=1 -v
```

Baseline measured 2026-09-10 on Docker 27.3.1/Linux amd64, PostgreSQL 17 Alpine, and a 6-core/12-thread Ryzen 5 5600H:

| Profile | Result |
|---|---:|
| Supported monitors | 900 at a 15-second interval in each of 3 regions |
| Completed checks | 2,700 / 2,700 in 10.23 seconds; 0 failed requests |
| Coordinator throughput | 263.98 check results/second |
| Claim/result HTTP p95 | 32.04 ms |
| Raw 30-day aggregate | 3.77 ms over 2,700 rows |

This profile supports 180 checks/second of scheduled demand (900 × 3 ÷ 15) with 4 clients per region. Treat 900 monitors/3 regions as the verified ceiling for this deployment class, not a universal limit; rerun on production-equivalent hardware before raising it. A 1,000-monitor comparison completed without request failures but took 15.68 seconds for a 15-second schedule, so it is not published as sustained capacity. A region failure leaves its outstanding leases isolated by region, so healthy regions continue. Offline detection is the configured 30 seconds (plus up to 10 seconds for the UI refresh). Region visibility recovers on the first claim; a monitor abandoned in flight is re-claimable within the two-minute lease plus the normal two-second poll.

Daily/monthly rollups remain deliberately absent: the measured raw query is below the 250 ms budget. Add rollups only after the production-sized profile exceeds 250 ms in three consecutive runs. Redis/another queue also remains absent: this profile had zero lease conflicts/request failures at 263.98 results/second. Reconsider a queue only if claim/result p95 exceeds 100 ms or lease-related failures exceed 1% in three repeatable runs.

### Backup

```bash
docker compose exec -T postgres pg_dump -U pulseops -d pulseops -Fc > pulseops.backup
```

### Restore

Restore into an empty PulseOps database while the API is stopped:

```bash
docker compose stop api
docker compose exec -T postgres pg_restore -U pulseops -d pulseops --clean --if-exists < pulseops.backup
docker compose start api
```

Test restores regularly and store backups away from the application host.

### Upgrade

```bash
git pull
docker compose up --build -d
```

The API applies pending migrations before accepting traffic.

### Rollback

PulseOps migrations are forward-only. Take a database backup and record the currently deployed image digest before every upgrade; application rollback without the matching database restore is unsupported.

```bash
# Before upgrading
docker image inspect pulseops-api --format '{{index .RepoDigests 0}}'
docker compose exec -T postgres pg_dump -U pulseops -d pulseops -Fc > pulseops-before-upgrade.backup

# Roll back application and data as one operation
docker compose stop api
docker compose exec -T postgres dropdb -U pulseops --force --if-exists pulseops_rollback
docker compose exec -T postgres createdb -U pulseops pulseops_rollback
docker compose exec -T postgres pg_restore -U pulseops -d pulseops_rollback --no-owner --no-privileges < pulseops-before-upgrade.backup
# Point DATABASE_URL at pulseops_rollback and pin the recorded pre-upgrade image.
docker compose up -d api
curl --fail http://localhost:3000/api/readyz
```

Keep the failed-upgrade database intact until the restored deployment is verified. The release-gate exercise restores into a separate empty database, checks migration head, sentinel data, and table count, then removes only its temporary databases.

### Webhook payload

When `ALERT_WEBHOOK_URL` is configured, PulseOps sends a JSON `POST` and retries non-2xx responses through the same PostgreSQL outbox used for email:

```json
{"event":"pulseops.alert","subject":"PulseOps incident: API","body":"API is down. HTTP 503"}
```

### Web Push

Set all three `WEB_PUSH_*` variables to enable browser alerts. Generate one VAPID key pair with a trusted Web Push tool, keep the private key secret, and retain the same pair across deployments. Each member enables notifications from the dashboard and selects exactly which monitors that browser should receive. Push jobs use the same outbox, escalation delay, idempotency, and retry behavior as email and webhooks; deleting the member key also removes its subscriptions and pending push jobs.

## Development

```bash
cd api
go test ./...

cd ../web
npm ci
npm test
npm run build
```

CI runs the same backend tests, frontend tests, and production build on every push and pull request. The capacity profile is opt-in because it needs an expendable database and is intended for production-equivalent sizing runs.

## Delivery status

The core platform is production-shaped: monitoring, incidents, team roles, visible regional workers, quorum decisions, audit history, reporting, notifications, OpenTelemetry traces, and a mobile PWA are implemented and container-verified. See [the roadmap](docs/roadmap.md) for release gates.

---

<div align="center">
Built to stay calm when production is not.
</div>
