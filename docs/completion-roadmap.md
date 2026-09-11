# PulseOps completion roadmap

This plan turns the development roadmap into small, verifiable delivery slices.
Each slice ends with code, tests, documentation, and one runnable acceptance check.

## Slice 1 — close Phase 1

- [x] Finish recurring maintenance schedules without per-monitor refresh traffic.
- [x] Re-evaluate maintenance state when a result is recorded and refresh UI state at
  schedule boundaries.
- [x] Add timezone-aware exception dates while preserving `maintenanceUntil`.
- [x] Keep the OpenAPI contract aligned with every maintenance route.
- [x] Cover migration head/idempotency, expired leases, quorum ties, and notification
  retry backoff with regression tests.
- [x] Run backend tests, frontend tests/build, and a fresh-schema Compose migration check.
- [x] Commit the slice and confirm the GitHub Actions run is green.

Exit: all Phase 1 roadmap boxes and its acceptance statement are demonstrably true.

## Slice 2 — team operations foundation

- [x] Add expiring invitation links using the existing organization and role model.
- [x] Add per-monitor notification channels and escalation delay fields.
- [x] Add status-page components and operator-authored incident updates.
- [x] Add optional Web Push subscriptions and delivery through the existing outbox.

Exit: two operators can use independently revocable access and receive only their
configured monitor notifications.

## Slice 3 — measured scale and observability

- [x] Add worker heartbeat visibility and region availability warnings first.
- [x] Establish repeatable coordinator/worker load tests and publish measured limits.
- [x] Add OpenTelemetry traces and example Tempo/Grafana deployment dashboards.
- [x] Measure raw-check queries and defer daily/monthly rollups below the 250 ms budget.
- [x] Measure PostgreSQL lease throughput and defer Redis while contention is unproven.

Exit: supported monitor count, check rate, failure behavior, and recovery time are
documented from repeatable tests.

Baseline: 900 monitors × 3 regions at 15 seconds, 263.98 results/second,
32.04 ms HTTP p95, zero failed requests, 30-second worker failure detection,
region recovery on the first claim, and abandoned-monitor recovery within the
two-minute lease plus the normal 2-second poll.

## Release gate

- CI is green from a clean checkout.
- Fresh install and upgrade migrations both reach head.
- Backup/restore and rollback instructions are exercised.
- OpenAPI, README, and roadmap match shipped behavior.
- No unresolved P1/P2 correctness, security, or scaling findings remain.
