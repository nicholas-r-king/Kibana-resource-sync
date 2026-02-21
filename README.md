# kibana-resource-sync

`kibana-resource-sync` is a Golang CLI/CronJob tool that promotes Kibana dashboards and associated alert rules from one source Kibana instance to multiple target instances based on metadata tags.

## What It Does

On each run, the tool:

1. Discovers source dashboards tagged with:
   - `Ready=True`
   - `Regions=<comma-separated-regions>`
   - `Environments=<comma-separated-environments>` when environment routing is enabled
2. Resolves destination(s) from config:
   - legacy mode: `region`
   - promotion mode: `environment + region`
3. Upserts dashboards to each mapped target by dashboard ID (idempotent).
   - rewrites referenced data-view IDs to destination-specific IDs using `data_views` config
   - preflights import payload/object size and applies `oversize_policy` (`error|chunk|skip`)
   - skips dashboard upsert if `source_hash=<sha256>` already matches in target
4. Syncs alert rules that are explicitly associated to the dashboard via:
   - `DashboardUID=<dashboard_id>`
   - skips rule upsert if `source_hash=<sha256>` already matches in target
5. Reconciles stale resources in targets when regions or environments are removed:
   - `reconcile_mode=delete` deletes stale dashboards/rules
   - `reconcile_mode=disable` disables stale rules and leaves dashboards in place (Kibana dashboard API does not support dashboard disable)
6. Adds provenance markers to managed resources:
   - `managed-by=kibana-resource-sync`
   - `source_uid=<dashboard_id>`
   - `source_stack=<source_name>`
   - `source_rule_id=<rule_id>` for rules
   - `source_hash=<sha256>`

## Current Drift Behavior

- `drift_mode=overwrite` (default): managed resources are overwritten each run.
- `drift_mode=flag`: if a managed resource already exists in target, update is skipped and a drift event is logged.

Hash-based skip remains safe for self-healing: when `source_hash` appears to match, the tool also computes a normalized target fingerprint (excluding managed provenance tags). If destination content drifted due manual edits, overwrite is forced in `drift_mode=overwrite`.

This provides the base needed for future bidirectional editing workflows.

## Prerequisites

- Go 1.26+
- Kibana service account/API token per instance
- Required Kibana permissions for dashboards, tags, and alerting rules in each configured space
- Docker + Docker Compose (for local end-to-end integration tests)

## Code Organization

Key internal packages are organized by concern:

- `internal/sync`: run orchestration and target/source reconciliation flow
- `internal/kibana`: Kibana API client and transport models
- `internal/tags`: tag parsing and tag mutation helpers
- `internal/fingerprint`: deterministic hashing for dashboards/rules and normalized hash inputs
- `internal/dashboards`: import payload sizing, ordering, and chunk planning
- `internal/ndjson`: NDJSON parse/encode and saved object helpers
- `internal/config`, `internal/logging`, `internal/metrics`: config loading, logging, and run metrics

## Configuration

Copy and edit `config.example.yaml`.

### Authentication Per Instance

Each configured Kibana instance (`source`, `targets.*`, or `environments.*.*`) must use exactly one auth mode:

- Token auth:
  - primary: `api_token` or `api_token_env`
  - optional fallback: `api_token_secret_id` and optional `api_token_secret_key`
- Basic auth:
  - primary: `username` or `username_env`
  - primary: `password` or `password_env`
  - optional fallback:
    - `username_secret_id` and optional `username_secret_key`
    - `password_secret_id` and optional `password_secret_key`

Notes:

- You cannot mix token auth and basic auth on the same instance config.
- When using basic auth, both username and password are required.
- `api_token`, `username`, and `password` support `env:VAR_NAME` and `${VAR_NAME}` forms.
- Resolution precedence per credential is: direct value -> `*_env` -> AWS Secrets Manager fallback.
- AWS Secrets Manager uses the default AWS SDK credential/region chain.

### Destination Routing Modes

You can use one of the following (not both in the same config):

- `targets` (legacy region-only routing):
  - Example keys: `eu`, `na`, `selfhost`
- `environments` (promotion routing with environment + region):
  - Example keys: `staging.eu`, `prod.na` via nested YAML:
  - `environments.<environment>.<region>`

### Data View Mapping

Dashboard exports usually contain references to data-view saved object IDs (`index-pattern`).
Those IDs often differ across environments, so `kibana-resource-sync` rewrites data-view IDs before import.

Configuration:

- `data_views.mode`
  - `strict` (default): require an explicit mapping or identical ID in destination
  - `assist`: if mapping is missing, attempt title-based auto-match in destination
- `data_views.mappings`
  - key = target key:
    - environment mode: `<environment>/<region>` (example `staging/eu`)
    - legacy mode: `<region>` (example `eu`)
  - `by_source_id`: source data-view ID -> destination data-view ID
  - `by_source_title`: source data-view title -> destination data-view ID

When a dashboard is synced:

1. all `index-pattern` references are resolved to destination IDs
2. `searchSourceJSON` index references are rewritten where needed
3. source `index-pattern` objects are excluded from import (destination data views stay authoritative)

### Oversize Handling

Large dashboard exports can exceed Kibana import limits. Use these config keys:

- `oversize_policy`
  - `error` (default): fail run when payload/object exceeds limits
  - `chunk`: split dashboard import into multiple smaller batches
  - `skip`: log warning and skip oversized dashboards
- `max_import_payload_bytes`
  - max bytes per import request (used for preflight and chunking)
- `max_saved_object_bytes`
  - max bytes for a single saved object line in NDJSON

In `--dry-run`, logs include:

- `import_object_count`
- `import_total_bytes`
- `import_max_object_bytes`
- `import_chunk_count`

### Tag Contract

Dashboards in source must include tags:

- `Ready=True`
- `Regions=selfhost,eu,na` (example)
- `Environments=staging,prod` when `environments:` routing is configured

Alert rules in source that should move with a dashboard must include:

- `DashboardUID=<dashboard_id>`

Optional rule tags:

- `Ready=True` (enforced only if `require_rule_ready: true`)
- `Regions=...` (otherwise rule inherits dashboard regions)
- `Environments=...` (otherwise rule inherits dashboard environments)

Fingerprint/provenance key names are configurable, including `source_hash_tag_key` (default `source_hash`).

## CLI Usage

```bash
# Dry-run (recommended first)
go run ./cmd/kibana-resource-sync \
  -config ./config.yaml \
  -dry-run \
  -log-level info

# Actual sync
./kibana-resource-sync -config ./config.yaml

# Override behavior at runtime
./kibana-resource-sync -config ./config.yaml -reconcile-mode disable -drift-mode flag
```

## Container Build

```bash
docker build -t kibana-resource-sync:latest .
```

## Kubernetes CronJob

Example manifest: `deploy/cronjob.yaml`

- Mount config as a ConfigMap at `/etc/kibana-resource-sync/config.yaml`
- Inject tokens via environment variables from Secret

## GitLab Scheduled Pipeline

Example pipeline job: `deploy/gitlab-ci.yml.example`

## Local Integration Test (Docker Compose)

This repository includes a full local integration harness with:

- source test stack: Elasticsearch + Kibana (`es-src` / `kibana-src`)
- destination test stack: Elasticsearch + Kibana (`es-dst` / `kibana-dst`)
- tool runner container (`sync`)
- automatic seed + verify jobs (`seed-source`, `verify`)

Files:

- Compose stack: `docker-compose.yml`
- Integration config: `deploy/integration/config.integration.yaml`
- Seed script: `deploy/integration/scripts/seed-source.sh`
- Verify script: `deploy/integration/scripts/verify-target.sh`

Run the integration test:

```bash
make compose-test
```

By default, the compose stack runs Elasticsearch/Kibana `9.3.0`.
To run a specific stack version, set `STACK_VERSION`:

```bash
# Validate v9 default pair explicitly
make compose-test-v9

# Validate v8 compatibility
make compose-test-v8

# Or use any supported version pair directly
STACK_VERSION=8.15.3 make compose-test
```

If your machine uses Docker Compose v2 plugin syntax, run:

```bash
make COMPOSE="docker compose" compose-test
```

This command:

1. starts both test Kibana/Elasticsearch clusters
2. seeds:
   - fake log documents in both Elasticsearch clusters (`demo-logs-*`)
   - source data-view `src-logs-data-view`
   - destination data-view `dst-logs-data-view`
   - source visualization `sync-demo-log-levels` (pie chart of `level.keyword`) using the source data-view
   - source dashboard tagged with `Ready=True`, `Regions=local`, `Environments=staging`, containing the visualization panel and referencing the source data-view
3. runs `kibana-resource-sync` in-container with mapping `src-logs-data-view -> dst-logs-data-view`
4. verifies the destination dashboard + visualization reference `dst-logs-data-view` (not `src-logs-data-view`), destination Elasticsearch has queryable docs, and provenance tags exist:
   - `managed-by=kibana-resource-sync`
   - `source_uid=<dashboard_id>`
   - `source_stack=dev`

Clean up:

```bash
make compose-down
```

## Logging and Metrics

- JSON structured logs to stdout
- End-of-run summary includes counters:
  - examined/eligible/upserted/deleted dashboards
  - examined/upserted/deleted/disabled rules
  - failure and drift counters
- Optional Pushgateway push from env vars:
  - `DASHBOARD_SYNC_PUSHGATEWAY_URL` (required to enable push)
  - `DASHBOARD_SYNC_PUSHGATEWAY_JOB` (optional, default `kibana-resource-sync`)
  - `DASHBOARD_SYNC_PUSHGATEWAY_INSTANCE` (optional grouping key)
  - `DASHBOARD_SYNC_PUSHGATEWAY_TIMEOUT` (optional, Go duration, default `5s`)

When enabled, the run summary counters and run status (`dashboard_sync_last_run_success`) are pushed at the end of each run.

These counters can be scraped from logs or forwarded to your metrics pipeline.

## Limitations and Notes

- Rule actions reference connector IDs; connectors must exist in each target Kibana or rule upsert will fail.
- `reconcile_mode=disable` does not disable dashboards (Kibana dashboard API does not expose disable semantics).
- API compatibility can differ by Kibana version; validate against your stack versions in `--dry-run` mode first.
