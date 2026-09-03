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
- **Content assertions** — optionally require expected text within the first 64 KiB of a successful HTTP response.
- **Incident lifecycle** — a failure opens one incident; recovery resolves it transactionally and keeps a dashboard timeline.
- **Flapping control** — configurable consecutive failure and recovery thresholds prevent noisy one-off alerts.
- **Maintenance windows** — continue collecting measurements while suppressing incident transitions and notifications.
- **Useful history** — response time, HTTP status, errors, 24-hour uptime, and 90-day check retention.
- **SSL awareness** — certificate expiry capture and configurable early warning.
- **Durable notifications** — PostgreSQL outbox with idempotency and retry backoff for Brevo email.
- **Generic webhooks** — send the same durable incident and SSL events to Slack-compatible relays or your own automation.
- **Live dashboard** — monitor CRUD, operational metrics, and Server-Sent Events updates.
- **Public status page** — only monitors explicitly marked public are exposed at `/status`.
- **Operational follow-through** — acknowledge incidents, attach operator notes, and publish recent public incident history.
- **30-day reporting** — inspect per-monitor uptime, average response time, and check volume for any 1–90 day window.
- **Scoped API keys** — create revocable read-only or read/write integration keys; secrets are stored as SHA-256 hashes and shown once.
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
| `SSL_WARNING_DAYS` | No | `14` | Certificate warning threshold |

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
| `GET/POST` | `/api/monitors` | Admin | List/create monitors |
| `PUT/DELETE` | `/api/monitors/{id}` | Admin | Update/delete a monitor |
| `GET` | `/api/monitors/{id}/checks` | Admin | Latest 100 checks |
| `GET` | `/api/incidents` | Admin | Latest 100 incidents |
| `PATCH` | `/api/incidents/{id}` | Write | Acknowledge an incident and save an operator note |
| `GET` | `/api/reports/uptime?days=30` | Read | Per-monitor uptime report for 1–90 days |
| `GET/POST` | `/api/keys` | Root token | List/create scoped API keys |
| `DELETE` | `/api/keys/{id}` | Root token | Revoke an API key |

For a heartbeat monitor, send a request after the job succeeds:

```bash
curl -X POST https://status.example.com/api/heartbeat/hb_your_one_time_secret
```

The heartbeat URL is displayed only when the monitor is created. Store it like a password. If it is lost, create a replacement monitor.

## Security model

PulseOps is designed for a trusted single-operator deployment. The server-configured root token manages API keys. Integration keys can be read-only or read/write, are stored only as hashes, and can be revoked independently. Public APIs expose only monitor health and incidents for monitors marked `public`, never credentials or notification settings. Endpoint validation rejects credentials in URLs, private/loopback/link-local targets, DNS resolutions to private networks, redirects beyond five hops, and TLS below 1.2.

Put internet-facing installations behind HTTPS, use unique secrets, restrict host access, and keep Docker/PostgreSQL patched. PulseOps intentionally does not provide multi-user accounts or organization-level authorization.

## Operations

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

### Webhook payload

When `ALERT_WEBHOOK_URL` is configured, PulseOps sends a JSON `POST` and retries non-2xx responses through the same PostgreSQL outbox used for email:

```json
{"event":"pulseops.alert","subject":"PulseOps incident: API","body":"API is down. HTTP 503"}
```

## Development

```bash
cd api
go test ./...

cd ../web
npm ci
npm test
npm run build
```

CI runs the same backend tests, frontend tests, and production build on every push and pull request.

## Delivery status

The MVP is complete: HTTP/content and heartbeat monitoring, incidents, scoped API access, reliability reporting, SSL/Brevo/webhook notifications, live dashboard, public incident history, OpenAPI documentation, and production hardening are implemented and container-verified. See [the roadmap](docs/roadmap.md) for the verification criteria and sensible post-MVP options.

---

<div align="center">
Built to stay calm when production is not.
</div>
