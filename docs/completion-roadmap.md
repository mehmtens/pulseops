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

- Add expiring invitation links using the existing organization and role model.
- Add per-monitor notification channels and escalation delay fields.
- Add status-page components and operator-authored incident updates.
- Add optional Web Push subscriptions and delivery through the existing outbox.

Exit: two operators can use independently revocable access and receive only their
configured monitor notifications.

## Slice 3 — measured scale and observability

- Add worker heartbeat visibility and region availability warnings first.
- Establish repeatable coordinator/worker load tests and publish measured limits.
- Add OpenTelemetry traces and example deployment dashboards.
- Add daily/monthly rollups only after raw-check queries cross a measured budget.
- Introduce Redis or another queue only if PostgreSQL lease contention is proven.

Exit: supported monitor count, check rate, failure behavior, and recovery time are
documented from repeatable tests.

## Release gate

- CI is green from a clean checkout.
- Fresh install and upgrade migrations both reach head.
- Backup/restore and rollback instructions are exercised.
- OpenAPI, README, and roadmap match shipped behavior.
- No unresolved P1/P2 correctness, security, or scaling findings remain.
