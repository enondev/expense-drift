# ExpenseDrift Operations Runbook

Operational procedures for deploying, maintaining, backing up, and recovering **ExpenseDrift** across Cloudflare D1/KV, Backblaze B2 / Tigris, GitHub Actions, and SnapDeploy.

---

## 0. Environment Variables

ExpenseDrift is configured entirely via environment variables. Create a `.env` file locally or configure secrets in GitHub Actions / SnapDeploy.

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `CF_ACCOUNT_ID` | yes | — | Cloudflare account ID |
| `CF_API_TOKEN` | yes | — | Cloudflare API token (used as Bearer auth for D1/KV) |
| `CF_D1_DATABASE_ID` | yes | — | Cloudflare D1 database ID |
| `CF_KV_NAMESPACE_ID` | yes | — | Cloudflare KV namespace ID |
| `LLM_PROVIDER` | no | `local` | `local`, `byok`, or `off` |
| `LLM_BASE_URL` | no | `http://127.0.0.1` | Local OpenAI-compatible endpoint (Ollama / llama.cpp) |
| `LLM_MODEL` | no | `qwen2.5:7b-instruct` | LLM model name |
| `LLM_API_KEY` | no | — | Required only when `LLM_PROVIDER=byok` |
| `S3_ENDPOINT` | no | `https://backblazeb2.com` | S3-compatible endpoint (Backblaze B2 or Tigris) |
| `S3_BUCKET` | no | `expensedrift-archive` | Cold archive bucket name |
| `S3_ACCESS_KEY_ID` | no | — | S3 access key |
| `S3_SECRET_ACCESS_KEY` | no | — | S3 secret key |
| `PORT` | no | `8080` | HTTP listen port for `cmd/server` |
| `ADMIN_USER` | no | `admin` | Basic Auth username for UI routes |
| `ADMIN_PASSWORD` | no | `change_me_strong_password` | Basic Auth password for UI routes |
| `TRIGGER_SECRET` | no | `change_me_random_bearer_token` | Bearer token for `/api/v1/trigger/*` endpoints |

Create `.env` locally:

```bash
# Cloudflare D1 & KV
CF_ACCOUNT_ID="..."
CF_API_TOKEN="..."
CF_D1_DATABASE_ID="..."
CF_KV_NAMESPACE_ID="..."

# LLM Extraction
LLM_PROVIDER="local"
LLM_BASE_URL="http://127.0.0.1"
LLM_MODEL="qwen2.5:7b-instruct"

# Cold Storage
S3_ENDPOINT="https://backblazeb2.com"
S3_BUCKET="expensedrift-archive"
S3_ACCESS_KEY_ID="..."
S3_SECRET_ACCESS_KEY="..."

# Control Plane Security
ADMIN_USER="admin"
ADMIN_PASSWORD="..."
TRIGGER_SECRET="..."
```

---

## 1. Initial Setup & Migrations

### Apply D1 Schema Migrations

Run from the repository root whenever `migrations/schema.sql` is updated:

```bash
npx wrangler d1 execute expensedrift --remote --file=./migrations/schema.sql
```

### Seed Initial Vendors & Watch Targets

Load `schemas/saas_seeds.json` via the collector CLI, or apply `migrations/seeds.sql` directly to D1 via wrangler:

```bash
# Option A: via collector CLI
go run ./cmd/collector --seed-only --seeds ./schemas/saas_seeds.json

# Option B: via wrangler SQL (recommended for first-time provisioning)
npx wrangler d1 execute expensedrift --remote --file=./migrations/seeds.sql
```


### Build & Smoke-Test the Multi-Stage ARM64 Container Locally

Verify the static binary, embedded assets, and TLS root certificates inside Docker before deploying to SnapDeploy:

```bash
# Cross-compile linux/arm64 image without QEMU overhead
docker build --platform linux/arm64 -t expensedrift:latest .

# Smoke-test the HTTP control plane
docker run --rm -p 8080:8080 --env-file .env expensedrift:latest
curl -i http://localhost:8080/healthz

# Optional: run a one-off collector dry-run using the same image
docker run --rm --env-file .env --entrypoint /app/expensedrift-collector \
  expensedrift:latest --dry-run --target vercel:pricing
```

---

## 2. Production Deployment

### Option A: Render Free Tier (On-Demand Control Plane)

Render's free tier enforces spin-down after 15 minutes of inactivity, 512 MB RAM, and automatic PORT assignment. The `cmd/server` binary binds to `0.0.0.0:$PORT` and spins up on request.

**1. Connect your repo** in the Render dashboard (or via `render.yaml` blueprint).

**2. Configure environment variables** (set these in the Render dashboard, not in the repo):

| Variable | Value |
|----------|-------|
| `TRIGGER_SECRET` | High-entropy bearer token for `/api/v1/trigger/*` |
| `ADMIN_USER` | Basic Auth username for UI routes |
| `ADMIN_PASSWORD` | Basic Auth password for UI routes |
| `LLM_PROVIDER` | `local` |
| `LLM_BASE_URL` | `http://127.0.0.1` |
| `LLM_MODEL` | `qwen2.5:7b-instruct` |

Render assigns `PORT` automatically — do not set it manually.

**3. Deploy:** Render builds the Docker image and deploys the web service. Verify:

```bash
curl -i https://<your-service>.onrender.com/healthz
```

**Memory footprint:** `< 45 MB` RSS (bounded concurrency, 5 MB page read limit, zero headless browsers).

### Option B: SnapDeploy Free Tier (On-Demand Control Plane)

SnapDeploy's free tier enforces a 15-minute idle auto-sleep, 100 container-hours/month, 512 MB RAM, `linux/arm64` architecture, and browser-only wake. The `cmd/server` binary is designed to sleep when idle and wake on browser visit.

**1. Build the multi-stage ARM64 image:**

```bash
docker build --platform linux/arm64 -t expensedrift:latest .
```

**2. Push to your registry** (or deploy directly via SnapDeploy's Git integration):

```bash
docker tag expensedrift:latest <registry>/expensedrift:latest
docker push <registry>/expensedrift:latest
```

**3. Deploy to SnapDeploy** with the environment variables from Section 0. Bind to `0.0.0.0:$PORT` (default 8080).

**4. Verify:**

```bash
curl -i https://<your-app>.snapdeploy.app/healthz
```

**Memory footprint:** `< 45 MB` RSS (bounded concurrency, 5 MB page read limit, zero headless browsers).

### Option B: GitHub Actions Scheduled Collector (Headless Batch Worker)

`cmd/collector` runs on a schedule via GitHub Actions so collection runs hands-free without keeping a container awake 24/7. See `.github/workflows/collect.yml`.

**Required GitHub Actions secrets:**
- `CF_ACCOUNT_ID`
- `CF_API_TOKEN`
- `CF_D1_DATABASE_ID`
- `CF_KV_NAMESPACE_ID`
- `LLM_PROVIDER` (optional, default `local`)
- `LLM_BASE_URL` (optional, default `http://127.0.0.1`)
- `LLM_MODEL` (optional, default `gpt-oss-20b`)
- `LLM_API_KEY` (required only if `LLM_PROVIDER=byok`)
- `S3_ENDPOINT`, `S3_BUCKET`, `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY` (required for cold archival)

The workflow:
1. Checks out the repo and sets up Go
2. Installs Wrangler and applies the D1 schema migration
3. Seeds vendors/watch targets from `schemas/saas_seeds.json`
4. Runs `go run ./cmd/collector` (bounded by cron, default every 6 hours)
5. Uploads the collection summary as an artifact

### Health Checks

- `GET /healthz` — unauthenticated liveness probe, returns `{"status":"ok","version":"0.1.0"}`
- SnapDeploy wakes the container on browser visit; automated crons/webhooks receive `503` during sleep (by design)

---

## 3. Routine Operations

### Trigger an Immediate Burst Collection (SnapDeploy / Local)

When SnapDeploy is awake (after visiting the dashboard URL in a browser), trigger a collection run via Bearer token:

**All active targets (default):**
```bash
curl -X POST "https://<your-app>.snapdeploy.app/api/v1/trigger/collect" \
  -H "Authorization: Bearer ${TRIGGER_SECRET}" \
  -H "Content-Type: application/json" \
  -d '{}'
```

**Single vendor:**
```bash
curl -X POST "https://<your-app>.snapdeploy.app/api/v1/trigger/collect" \
  -H "Authorization: Bearer ${TRIGGER_SECRET}" \
  -H "Content-Type: application/json" \
  -d '{"vendor_id": "vercel", "force_semantic": false}'
```

**Single target:**
```bash
curl -X POST "https://<your-app>.snapdeploy.app/api/v1/trigger/collect" \
  -H "Authorization: Bearer ${TRIGGER_SECRET}" \
  -H "Content-Type: application/json" \
  -d '{"target_id": "vercel:pricing"}'
```

Omitting both `vendor_id` and `target_id` runs every active watch target in one batch (bounded by `maxConcurrency <= 5`).

Locally:

```bash
# All active targets
go run ./cmd/collector

# Single vendor
go run ./cmd/collector --vendor vercel

# Single target
go run ./cmd/collector --target vercel:pricing
```

#### Troubleshooting: D1 "Expected object, received array" error

If you see `d1 query status 400: {"success":false,"errors":[{"code":7400,"message":"Invalid input: Expected object, received array|..."}]}`:

1. **Check your D1 API version** — Cloudflare may have changed the `/query` endpoint to expect `{"statements": [...]}` wrapper instead of a bare array `[...]`.
2. **Ensure `params` is never an empty array** — if a statement has no parameters, omit the `params` field entirely (the `omitempty` JSON tag should handle this, but verify).
3. **Test the D1 endpoint directly** with wrangler first:
   ```bash
   npx wrangler d1 execute expensedrift --remote --command="SELECT 1"
   ```
4. **Check Cloudflare D1 API docs** for the current request format — the `Statement` struct in `internal/storage/storage.go` sends `{"sql": "...", "params": [...]}` per statement; if Cloudflare now requires `{"statements": [{"sql": "...", "params": [...]}]}`, update `d1.go` accordingly.

### Force a Manual Cold-Storage Archive (D1 -> Backblaze B2 / Tigris)

If D1 approaches its 500 MB free-tier database limit, force an immediate offload of snapshots older than 14 days:

```bash
curl -X POST "https://<your-app>.snapdeploy.app/api/v1/trigger/archive" \
  -H "Authorization: Bearer ${TRIGGER_SECRET}" \
  -H "Content-Type: application/json" \
  -d '{"retention_days": 14}'
```

**Verification:** Confirm `exported_rows == deleted_rows` and `bytes_written > 0` in the JSON response before pruning local logs.

---

## 4. Incident Playbooks

### Incident A: False-Positive Diffs After Vendor Page Redesign

**Symptom:** A vendor overhauls its frontend CSS/JS, causing noisy or malformed `BillingRuleSet` extractions.

**Fix:**
1. Inspect the raw output using `cmd/collector` in dry-run mode:
   ```bash
   go run ./cmd/collector --dry-run --target vercel:pricing
   ```
2. Update `extract_selector` or bump `fetch_tier` (`1` -> `2` or `3`) for that target in D1:
   ```bash
   npx wrangler d1 execute expensedrift --remote \
     --command="UPDATE watch_targets SET fetch_tier = 2, extract_selector = 'main' WHERE id = 'vercel:pricing';"
   ```
3. Acknowledge the noisy diff via `POST /api/v1/diffs/{diff_id}/ack`.

### Incident B: Broken Composite Audit Hash Chain (`prev_composite_hash` Mismatch)

**Symptom:** A manual DB edit or partial failed write causes `composite_audit_hash` continuity validation to fail on a target.

**Fix:** Re-anchor the target's active hashes from its latest valid snapshot in D1 and refresh `manifest:v1` in KV:

```bash
npx wrangler d1 execute expensedrift --remote --command="
UPDATE watch_targets
SET last_raw_hash = (SELECT raw_content_hash FROM snapshots WHERE target_id = 'vercel:pricing' ORDER BY captured_at DESC LIMIT 1),
    last_semantic_hash = (SELECT semantic_schema_hash FROM snapshots WHERE target_id = 'vercel:pricing' ORDER BY captured_at DESC LIMIT 1),
    last_composite_hash = (SELECT composite_audit_hash FROM snapshots WHERE target_id = 'vercel:pricing' ORDER BY captured_at DESC LIMIT 1)
WHERE id = 'vercel:pricing';
"
```

Then rebuild the single-key KV manifest (`manifest:v1`):

```bash
go run ./cmd/collector --rebuild-manifest
```

### Incident C: Local LLM (`Ollama` / `llama.cpp`) Unreachable During Scheduled Run

**Symptom:** `extractor_model` records `"deterministic"` instead of `"ollama/qwen2.5:7b-instruct"`.

**Behavior:** Expected graceful degradation. No data is lost; `raw_content_hash` is still recorded. Once the local LLM daemon is back online, re-run with `"force_semantic": true` for affected targets if deeper NLP extraction is needed.

### Incident D: D1 Daily Read/Write Quota Warning

**Symptom:** Cloudflare dashboard shows >80% usage of the 5M daily row reads or 100k daily row writes.

**Fix:**
1. Verify `manifest:v1` in KV is populated (`--rebuild-manifest`) so unchanged targets exit before querying D1.
2. Check for unindexed queries or high-frequency external polling against `/api/v1/diffs`.

---

## 5. Disaster Recovery & Cold Analytics

### Full D1 SQL Backup

Export the entire hot D1 database directly via Wrangler:

```bash
npx wrangler d1 export expensedrift --remote --output=./backup-$(date +%F).sql
```

### Query Historical Cold Archives via DuckDB

Run ad-hoc longitudinal pricing queries directly against Backblaze B2 or Tigris without restoring rows into D1:

```sql
INSTALL httpfs;
LOAD httpfs;

SET s3_endpoint='://backblazeb2.com';
SET s3_access_key_id='<S3_ACCESS_KEY_ID>';
SET s3_secret_access_key='<S3_SECRET_ACCESS_KEY>';
SET s3_url_style='path';

SELECT
  vendor_id,
  target_id,
  captured_at,
  json_extract_string(canonical_json, '\$.tiers[0].base_price_monthly') AS starter_price,
  json_extract_string(canonical_json, '\$.tiers[0].credit_multiplier') AS credit_multiplier
FROM read_json_auto('s3://expensedrift-archive/snapshots/**/*.jsonl.gz')
WHERE vendor_id = 'linear'
ORDER BY captured_at ASC;
```
