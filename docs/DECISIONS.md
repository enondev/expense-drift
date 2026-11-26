# ExpenseDrift Architectural Decision Log (ADR)

Consolidated log of key architectural choices and free-tier constraints so future changes do not re-introduce cost or resource traps.

---

## ADR-001: Split Scheduled Collector (GitHub Actions) from Control Plane (SnapDeploy)
- **Status:** Accepted
- **Context:** SnapDeploy's free tier enforces a 15-minute idle auto-sleep, 100 container-hours/month, 512 MB RAM, `linux/arm64` architecture, and **browser-only wake** (automated webhooks/crons receive `503` wake pages).
- **Decision:** Compile two binaries from one Go codebase:
  1. `cmd/collector`: Headless batch worker executed on a schedule via GitHub Actions (or local cron).
  2. `cmd/server`: Authenticated HTTP UI and manual burst runner hosted on SnapDeploy, waking only when opened in a browser.
- **Consequence:** Zero dropped scheduled runs, ~0 idle container-hours burned, and zero webhook `503` failures.

## ADR-002: Hot Storage on Cloudflare D1 + Single-Key KV Manifest
- **Status:** Accepted
- **Context:** Durable Objects require fan-out across isolated SQLite instances for cross-vendor queries, and Cloudflare KV free tier caps writes at 1,000/day. D1 provides 500 MB/DB, 5M row reads/day, and 100k row writes/day with no credit card required.
- **Decision:** Store normalized vendors, watch targets, 30-day snapshots, and billing diffs in D1 with strict covering indexes. Store a single JSON hash map under KV key `manifest:v1` (1 read at batch start, 1 write at batch end).
- **Consequence:** Unchanged pages exit after 1 KV read (`0` D1 reads/writes), keeping daily D1 usage under 5% of free quotas.

## ADR-003: Zero-Bill-Risk Cold Storage (Backblaze B2 / Tigris) Over Cloudflare R2
- **Status:** Accepted
- **Context:** Cloudflare R2 requires a payment method on file and bills per-operation/storage overages. Backblaze B2 (10 GB free) and Tigris (5 GB free, unlimited egress) require no credit card and hard-stop at free limits.
- **Decision:** Stream snapshots older than 30 days from D1 via `io.Pipe` + `gzip` to standard S3-compatible endpoints (`aws-sdk-go-v2`), then prune D1 rows after verifying `bytes_written > 0`.
- **Consequence:** Guaranteed `$0` surprise billing while preserving multi-year longitudinal archives queryable via DuckDB `httpfs`.

## ADR-004: Tiered HTTP/Hydration Fetcher Without Headless Browsers
- **Status:** Accepted
- **Context:** Headless Chromium/Playwright routinely consumes 300–600 MB RAM, exceeding SnapDeploy's 512 MB container ceiling.
- **Decision:** Use a bounded Go worker pool (`maxConcurrency <= 5`, 5 MB `io.LimitReader` cap) with a 3-tier waterfall: (1) embedded hydration JSON (`__NEXT_DATA__`, `__NUXT__`, `application/ld+json`), (2) `goquery` semantic DOM extraction, (3) optional external render fallback.
- **Consequence:** Runtime RSS stays under 45 MB.

## ADR-005: 3-Hash Merkle Cascade for Noise Elimination & Audit Lineage
- **Status:** Accepted
- **Context:** Marketing copy and CSS classes change frequently without altering commercial terms, while contract negotiations require tamper-evident historical proof.
- **Decision:** Compute `raw_content_hash` -> `semantic_schema_hash` -> `composite_audit_hash` (chaining `prev_composite_hash`). Only fire `billing_diffs` alerts when `semantic_schema_hash` changes.
- **Consequence:** Eliminates CSS-redesign false positives and redundant LLM calls.

## ADR-006: Local-First LLM Extraction with BYOK Opt-In & Deterministic Fallback
- **Status:** Accepted
- **Context:** Sending every changed page to paid cloud LLMs creates recurring API costs and vendor lock-in.
- **Decision:** Default `internal/extractor` to local OpenAI-compatible servers (`Ollama` on `:11434/v1` or `llama.cpp` on `:8080/v1`) with `temperature=0` and `schemas/billing_ruleset.schema.json`. Support opt-in `LLM_PROVIDER=byok` and degrade gracefully to deterministic JSON-LD/table parsing when offline.
- **Consequence:** Works completely free locally or on self-hosted runners, while remaining functional in headless CI without GPU/LLM access.