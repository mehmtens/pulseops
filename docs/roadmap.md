# PulseOps development roadmap

This roadmap keeps PulseOps small enough to operate while closing the gaps that
matter for a real production team. Every phase has a runnable acceptance check.

## Shipped

- [x] HTTP/HTTPS, content, TCP, DNS, and cron-heartbeat monitors.
- [x] PostgreSQL scheduling, safe leases, incident lifecycle, flapping controls,
  maintenance windows, response history, 90-day retention, and CSV reports.
- [x] Brevo email and generic webhook notifications with durable retries.
- [x] Public status page, incident timeline, branding, SSE refresh, OpenAPI 3.1,
  Docker Compose, health/readiness checks, and CI.
- [x] Installable mobile PWA with offline shell and application metadata.
- [x] Viewer/operator/admin team roles, organization profile, and revocable keys.
- [x] Regional workers with signed bearer access, one-time leases, region-tagged
  checks, and configurable multi-region quorum.
- [x] Admin audit trail for successful monitor, incident, key, and organization
  mutations.

## Phase 1 — harden the current platform (next)

- [x] Add audit retention and a CSV export endpoint so audit data cannot grow
  without bound and can be reviewed during an incident.
- [x] Add notification delivery metrics (queued, delivered, retrying) to the
  dashboard and API.
- [x] Add recurring maintenance schedules (weekly windows and timezone-aware
  exceptions) while preserving the current one-off window field.
- [x] Add regression tests for migration upgrades, lease expiry, quorum ties,
  and notification retry behavior.

Acceptance: a fresh Compose deployment reaches migration head, audit export is
bounded, and the full Go/frontend test suites pass in CI.

## Phase 2 — team operations

- [x] Add invitations and expiring member access links on top of the existing
  token roles.
- [x] Add per-monitor notification policies and escalation delays.
- [x] Add status-page incident updates and component grouping.
- [x] Add optional Web Push notifications for the existing PWA.

Acceptance: two operators can work on different monitors, receive only the
configured alerts, and revoke access without restarting the service.

## Phase 3 — scale when measured

- [x] Measure the raw-check query budget; defer daily/monthly rollups while the
  30-day query remains below 250 ms (3.77 ms in the baseline profile).
- [x] Add worker health/heartbeat visibility and region availability warnings.
- [x] Add OpenTelemetry traces and a provisioned Tempo/Grafana deployment dashboard.
- [x] Measure PostgreSQL lease throughput; retain PostgreSQL while contention is
  unproven (zero failures at 263.98 results/second in the baseline profile).

Acceptance: load tests document the monitor count, check rate, and recovery
behavior supported by one coordinator and a worker fleet.

Baseline acceptance: 900 monitors checked every 15 seconds from three regions,
2,700/2,700 results accepted, HTTP p95 32.04 ms, and 263.98 results/second on the
documented Docker Desktop host. See README “Measured capacity” for the repeatable
command, failure behavior, recovery bounds, and scale-up gates.

## Deliberately deferred

- Native iOS/Android apps, ICMP checks, browser multi-step journeys, and SSO.
- Full cross-tenant isolation and enterprise billing.

These are intentionally deferred until a deployment or customer requires the
additional operational surface.
