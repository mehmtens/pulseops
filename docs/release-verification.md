# Release verification

Last exercised: 2026-09-10

## Automated gate

An isolated Git snapshot was created from the complete release source with no working-tree changes. The GitHub Actions-equivalent commands passed from that snapshot:

- Go unit and PostgreSQL integration tests: pass.
- Frontend clean install (`npm ci`), Vitest suite, and production build: pass.
- Base plus observability Compose configuration: pass.
- OpenAPI JSON and provisioned Grafana dashboard JSON parsing: pass.
- Isolated load profile: 900 monitors across 3 regions, 2,700/2,700 results in 10.23 seconds, 263.98 results/second, 32.04 ms HTTP p95, and zero failures.

The hosted GitHub Actions result remains pending until the release changes are committed and pushed to the remote repository. Do not substitute this local result for the hosted check on a protected branch.

## Migration gate

- Fresh database: the release API applied migrations 1–14 and `worker_heartbeats` existed at head.
- Upgrade database: the integration suite applied migrations through v12, inserted a sentinel monitor, upgraded through v14, and verified both the sentinel and new table.
- A second migration call at head remained idempotent.

## Backup, restore, and rollback gate

A temporary PostgreSQL 17 source database was migrated to v14 and seeded with `release-gate-sentinel`. `pg_dump -Fc` was restored into a separate empty database with `pg_restore --clean --if-exists --no-owner --no-privileges`.

Source and restored databases matched:

| Check | Source | Restored |
|---|---:|---:|
| Migration head | 14 | 14 |
| Sentinel rows | 1 | 1 |
| Public tables | 15 | 15 |

The temporary API container, both temporary databases, and the in-container dump were removed after verification. README documents the same restore mechanism as the data half of the rollback procedure; rollback pins the pre-upgrade image and restores the pre-upgrade backup as one operation.

## Contract and findings gate

All 36 registered HTTP method/path pairs are represented in OpenAPI after adding the four previously missing operations. README role labels now match the enforced read/write/admin middleware, and both roadmap files match shipped/deferred scale behavior.

The requested Bugbot and Security Review subagent launchers were unavailable in this session, so two manual fallback passes were performed over the complete branch diff with `go vet`, `govulncheck`, `npm audit`, integration tests, and deployment configuration inspection.

The initial fallback reviews found eight P1/P2 issues. All were corrected:

| Priority | Area | Resolution |
|---|---|---|
| P1 | Web Push | Delivery uses the SSRF-safe resolver/transport, TLS 1.2 minimum, and a 10-second client/response timeout. A regression test rejects loopback destinations. |
| P1 | Dependencies | pgx and the complete OpenTelemetry/OTLP/gRPC/x-text set were upgraded. A repeated `govulncheck` reports zero called vulnerabilities. |
| P1 | Worker recovery | Claim 429/5xx responses and transient result failures now retry with bounded delay; permanent 4xx configuration failures still fail fast. |
| P1 | Grafana database access | A one-shot initializer provisions `pulseops_grafana` with SELECT-only rights. A live PostgreSQL exercise confirmed SELECT succeeds and INSERT is denied. |
| P2 | Worker status | Result heartbeat/counters update only after lease validation and successful check recording; an expired-lease regression test confirms the result count stays zero. |
| P2 | Recovery contract | Documentation now distinguishes region recovery on first claim from the two-minute worst-case abandoned-monitor lease recovery. |
| P2 | Dashboard threshold | Each heartbeat stores the configured offline threshold and Grafana evaluates the per-row value. |
| P2 | Invitation URL | The secret is carried in a URL fragment that is never sent to the server and is removed from browser history on load. Caddy/HTML also set `no-referrer`, and the service worker refuses to cache query URLs. |

After correction, `npm audit --omit=dev --audit-level=high` and `govulncheck` reported zero applicable vulnerabilities. `go vet ./...` passed. No unresolved P1/P2 correctness, security, or scaling findings remain in the reviewed diff.
