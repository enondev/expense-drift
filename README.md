# ExpenseDrift

**B2B SaaS "Shadow Inflation" & Billing-Rule Tracker**

ExpenseDrift monitors B2B SaaS pricing pages, API rate-limit documentation, sitemaps, and terms of service to detect **shadow inflation**—quiet packaging shifts that raise effective customer costs without necessarily changing headline list prices:

- **Credit multipliers & metering shifts** (e.g., per-seat to AI credit bundles or 1.5x action multipliers)
- **Hidden seat minimums & tier gating** (e.g., moving SSO, audit logs, or API access into higher plans)
- **API quota & rate-limit reductions** (e.g., cutting free or Pro request ceilings)
- **Contract & renewal traps** (e.g., extending auto-renewal notice windows or removing price-lock clauses)

Built as a lightweight, zero-fixed-cost Go system designed to run within free-tier infrastructure limits (Cloudflare D1/KV, Backblaze B2/Tigris, GitHub Actions, Render, and SnapDeploy).

---

## Architecture

ExpenseDrift splits scheduled collection from on-demand inspection so it never hits container cold-sleep webhook drops or exceeds 512 MB RAM limits:

```text
┌───────────────────────────┐      ┌─────────────────────────────────────┐
│   GitHub Actions (Cron)   │      │    Render / SnapDeploy Free Tier    │
│      cmd/collector        │      │             cmd/server              │
│  (Headless Batch Worker)  │      │  (Authenticated UI + Manual Runner) │
└─────────────┬─────────────┘      └──────────────────┬──────────────────┘
              │                                       │
              └───────────────────┬───────────────────┘
                                  ▼
                ┌───────────────────────────────────┐
                │   Tiered Fetcher & 3-Hash Engine  │
                │  • HTTP / Hydration JSON / DOM    │
                │  • Local LLM (Ollama/llama.cpp)   │
                │    + BYOK OpenAI-Compatible Opt-In│
                │  • Deterministic Table Fallback   │
                └─────────────────┬─────────────────┘
                                  │
               ┌──────────────────┴─────────────────┐
               ▼                                    ▼
┌─────────────────────────────────┐ ┌─────────────────────────────────┐
│     Cloudflare D1 + KV (Hot)    │ │   Backblaze B2 / Tigris (Cold)  │
│ • 1 KV read/write (manifest:v1) │ │ • Streaming Parquet / JSONL.gz  │
│ • Indexed SQLite tables (30d)   │ │ • Queryable via DuckDB httpfs   │
└─────────────────────────────────┘ └─────────────────────────────────┘
```

### Key Design Decisions

1. **3-Hash Merkle Cascade (`internal/hasher`):**
   - `raw_content_hash`: Normalized text/hydration JSON hash. Exits immediately if unchanged (`0ms`, zero DB/LLM cost).
   - `semantic_schema_hash`: Deterministic SHA-256 over canonicalized `BillingRuleSet` JSON. Prevents false-positive alerts when vendors redesign CSS/HTML without altering pricing rules.
   - `composite_audit_hash`: Links `prev_composite_hash` into a tamper-evident per-target audit chain for renewal negotiations.
2. **Local-First LLM Extraction (`internal/extractor`):**
   - Defaults to local OpenAI-compatible inference (`Ollama` at `:11434/v1` or `llama.cpp` at `:8080/v1`) with `temperature=0` and strict JSON schema output.
   - Supports opt-in external OpenAI-compatible endpoints (`LLM_PROVIDER=byok`) and degrades cleanly to a deterministic JSON-LD/HTML table parser (`LLM_PROVIDER=off`) when no LLM daemon is available.
3. **Hot-to-Cold Storage Compaction (`internal/storage`):**
   - Keeps active state and the last 30 days of snapshots in **Cloudflare D1** (using a single KV key `manifest:v1` to minimize D1 reads/writes).
   - Streams older snapshots via `io.Pipe` directly to **Backblaze B2** or **Tigris** (S3-compatible, no credit card required) for zero-compute SQL analytics in DuckDB.

---

## Repository Layout

```text
expensedrift/
├── AGENTS.md                        # Architectural & resource guardrails
├── Dockerfile                       # Multi-stage CGO-free ARM64 build (<25 MB)
├── go.mod
├── cmd/
│   ├── collector/main.go            # Headless batch runner (GitHub Actions / cron)
│   └── server/main.go               # Authenticated Control Plane UI (SnapDeploy)
└── internal/
    ├── config/                      # Environment configuration
    ├── fetcher/                     # SSRF-safe HTTP client, hydration & DOM parsers
    ├── extractor/                   # Ollama/llama.cpp + BYOK + deterministic parser
    ├── hasher/                      # Canonicalizer & 3-hash Merkle cascade
    ├── differ/                      # Severity scorer & JSON patch generator
    ├── storage/                     # Cloudflare D1, single-key KV, S3 cold archiver
    └── web/                         # Embedded HTML/htmx control plane & auth middleware
```

---

## Quick Start

### Prerequisites

- **Go 1.23+**
- *(Optional)* **Ollama** or **llama.cpp** running locally (`ollama pull qwen2.5:7b-instruct`)

### 1. Configure Environment

Create a `.env` file with the secrets needed for local development:

```bash
# Cloudflare D1 & KV (required for collector)
CF_ACCOUNT_ID="your_cloudflare_account_id"
CF_API_TOKEN="your_cloudflare_api_token"
CF_D1_DATABASE_ID="your_d1_database_id"
CF_KV_NAMESPACE_ID="your_kv_namespace_id"

# Control Plane Security (cmd/server)
TRIGGER_SECRET="change_me_random_bearer_token"

# Cold Storage (optional for local dev)
S3_ENDPOINT="https://backblazeb2.com"
S3_BUCKET="expensedrift-archive"
S3_ACCESS_KEY_ID="your_key_id"
S3_SECRET_ACCESS_KEY="your_application_key"
```

Full environment variable reference: see [docs/RUNBOOK.md#environment-variables](./docs/RUNBOOK.md).

### 2. Run Locally

**Run a headless collection batch:**

```bash
go run ./cmd/collector --seeds ./schemas/saas_seeds.json
```

**Start the Control Plane UI (`http://localhost:8080`):**

```bash
go run ./cmd/server
```

---

## Testing

All test suites run offline using `net/http/httptest` fixtures:

```bash
go test ./...
```

---

## Deployment & Operations

Production deployment, environment provisioning, Docker image builds, GitHub Actions setup, cold-archive querying, backup/restore, and incident response are documented in **[docs/RUNBOOK.md](./docs/RUNBOOK.md)**.

---

## License

Apache-2.0
