# STATE.md — ExpenseDrift Project Handoff & Context

**Project Codename:** ExpenseDrift (`https://github.com`)
**Last Updated:** 2026-11-24
**Current Phase:** Phase 1 Complete -> Phase 2 Steps 1-4 Complete -> QA/Security Remediation Complete

---

## 1. Current Status Summary

All architectural decisions, database schemas, JSON/OpenAPI contracts, seed targets, and operational runbooks are finalized. **Phase 2 Steps 1 (Core Models, Hasher & Differ), 2 (Tiered Fetcher & Local-First Extractor), 3 (Storage Adapters), and 4 (Entrypoints, Embedded UI & CI/CD) are implemented.** Review findings SEC-02, EXT-01, DIFF-01, SPEC-02 from `context.xml` are resolved. QA/security remediation complete.

### Completed Artifacts
- `go.mod` — module `expensedrift`, go 1.23; deps: `golang.org/x/net`, `github.com/PuerkitoBio/goquery`, `github.com/aws/aws-sdk-go-v2` (+ config, credentials, service/s3)
- `.env.example` — Committed template with placeholder values (no secrets)
- `.env` — Neutralized (all credentials replaced with `CHANGE_ME_*` placeholders)
- `schemas/embed.go` — `//go:embed` of `llm_extraction_prompt.md` and `billing_ruleset.schema.json` (SEC-02 fix: self-contained distroless binary)
- `internal/model/billing_ruleset.go` — `BillingRuleSet`, `PricingTier`, `QuotaRule`, `ContractTerms` structs matching `schemas/billing_ruleset.schema.json`; `Normalize()` (sorts tiers/rate-limits with tie-break, dedupes/lowercases `gated_features`, applies defaults); `NormalizeRaw()` (strips nonce/csrf attrs, HTML comments, collapses whitespace); shared `JSONPatchOp`, `Patch`, `DriftType` constants, `BillingDiffResult`
- `internal/hasher/hasher.go` — 3-hash Merkle cascade: `RawContentHash` (uses `NormalizeRaw`), `SemanticSchemaHash`, `CompositeAuditHash` (chains `prev_composite_hash`), `Cascade`, `ValidateCascadeRecomputation`
- `internal/differ/differ.go` — domain-specific `Compare(prev, curr)` returning `(BillingDiffResult, bool)`: directional price hikes (severity 5 if >=20% or from free, else 3; price drops ignored), annual discount cuts (3), credit multiplier/metering shifts (4), seat minimum increases (4), feature removals (4), quota cuts (3; increases/unlimited ignored), auto-renewal extensions (4), price-lock removal (5), overage hard_cap->auto_bill (4), new/deleted tiers (2/4-5), RFC 6902 numeric index paths including deleted tiers (DIFF-01 fix). Deep-clones inputs before Normalize() so callers stay immutable (ARCH-01 fix).
- `internal/fetcher/fetcher.go` — SSRF-safe HTTP client (`net.Dialer` control fn blocking loopback/private/link-local/multicast incl. across redirects), `io.LimitReader(5 MB)` body cap, semaphore concurrency <= 5, per-host >= 2s politeness delay, `__NEXT_DATA__` + JSON-LD extraction, goquery DOM boilerplate stripping
- `internal/extractor/extractor.go` — OpenAI-compatible `/chat/completions` client (`temperature=0`, `response_format: json_schema`), default base URL `http://127.0.0.1`, `LLM_PROVIDER=local|byok|off`, graceful deterministic fallback, embedded schemas via `schemas` package (SEC-02 fix)
- `internal/extractor/deterministic.go` — Zero-cost JSON-LD Offer/Product parser (string price support, EXT-01 fix) + HTML text price-pattern fallback (comma-separated prices, `/year` annual→monthly conversion, exact byte-offset tier context, EXT-01 fix)
- `internal/storage/storage.go` — Shared D1 config, `Statement`/`Result`/`QueryResponse` types, `Config.FromEnv()`, `ValidateStatements()` (every SELECT must filter on an indexed column to protect the 5M daily row-read cap, ADR-002), `countingWriter`, `ObjectKeyForRetention()`
- `internal/storage/d1.go` — Cloudflare D1 HTTP client (`POST /accounts/{id}/d1/database/{id}/query`, Bearer auth), batched statement execution wrapped in `{"statements": [...]}` per Cloudflare D1 API format, base URL injectable for tests
- `internal/storage/kv.go` — Single-key KV manifest manager (key `manifest:v1`, GET/PUT via Cloudflare KV REST API), 1 read at collector startup + 1 write at completion, 404->empty manifest, base URL injectable for tests
- `internal/storage/archive.go` — Streaming S3 cold archiver (`aws-sdk-go-v2`, custom endpoint + path-style for B2/Tigris), gzip.NewWriter -> io.Pipe -> s3.PutObject (flat RSS <60 MB), countingWriter byte counter, zero-bytes abort guard, `s3API` interface for offline mocking
- `internal/collector/collect.go` — Shared fetch → hash → compare → extract → store pipeline used by both `cmd/collector` and `cmd/server` (deduplication of business logic); per-target cooldown enforcement (`watch_targets.cooldown_seconds` / `next_check_at`) so targets aren't re-fetched until their cooldown elapses; D1 connectivity check on startup; `updateTargetCheckTime` updates `last_checked_at`/`next_check_at` after each target is processed
- `internal/web/server.go` — HTTP control plane: `/healthz`, `/api/v1/trigger/collect` (now wired to `collector.Run`, real diff detection), `/api/v1/trigger/archive` (now wires to `S3Archiver`, real cold archival), `/api/v1/vendors`, `/api/v1/diffs`, `/api/v1/diffs/{id}/ack`, `/api/v1/snapshots/{id}/latest` (Basic Auth), `/`, `/diffs` UI (Basic Auth or CF Access JWT), embedded `html/template` + `htmx` dashboard
- `cmd/collector/main.go` — Headless batch worker: fetch→hash→compare→extract→store pipeline; flags `--seed-only`, `--dry-run`, `--rebuild-manifest`, `--vendor`, `--target`, `--force-semantic`, `--seeds`; `slog` structured logging; fixed nil `summary` dereference and `DurationMS` calculation; `runSeedOnly` implemented
- `cmd/server/main.go` — Authenticated HTTP control plane; binds `0.0.0.0:$PORT` (default 8080). D1 schema and seeds provisioned separately via `migrations/schema.sql` and `migrations/seeds.sql` (wrangler). `slog` structured logging; `fmt` removed in favor of `slog`
- `schemas/saas_seeds.json` — Updated target URLs to specific `/pricing`, `/docs/limits`, `/terms`, `/legal/ssa` endpoints (DATA-01 fix)
- `migrations/schema.sql` — Cloudflare D1 schema & covering indexes
- `migrations/seeds.sql` — Seed INSERT statements for vendors + watch_targets, applied via wrangler for first-time D1 provisioning
- `AGENTS.md`, `STATE.md`, `docs/RUNBOOK.md`, `README.md`, `Dockerfile` — path/endpoint drift fixed: `schemas/saas_seeds.json`, `migrations/schema.sql`, `/api/v1/trigger/*`, `http://127.0.0.1` (SPEC-02 fix)
- `.github/workflows/collect.yml` — Scheduled headless collection (every 6 hours, cron `0 */6 * * *`), applies D1 migration, seeds targets via wrangler SQL, runs collector, uploads summary artifact
- `Dockerfile` — golang:1.23-alpine builder (was 1.26, aligned with AGENTS.md), distroless nonroot runtime, CA certificates copied
- Offline `httptest` unit tests for fetcher, extractor, storage, hasher, differ, model
- `go test ./...` and `go vet ./...` pass cleanly across all packages (9 packages)

---

## 2. Locked Technical Constraints (Must Preserve in Implementation)

1. **Dual-Binary Go Module:**
   - `cmd/collector`: Scheduled headless batch worker (GitHub Actions / local cron).
   - `cmd/server`: Authenticated HTTP control plane + manual runner (`SnapDeploy` or `Render` free tier, `linux/arm64`, `CGO_ENABLED=0`).
2. **Storage Topology:**
   - **Hot State:** Cloudflare D1 + single KV key `manifest:v1` (1 read + 1 write per collector run).
   - **Cold Archive:** Streaming `jsonl.gz` / Parquet via `io.Pipe` to Backblaze B2 or Tigris (`aws-sdk-go-v2`), pruning D1 rows >30 days old after byte-count verification.
3. **3-Hash Merkle Cascade:**
   - `raw_content_hash` -> `semantic_schema_hash` -> `composite_audit_hash` (`SHA256(vendor_id|prev_composite_hash|raw|semantic|timestamp)`).
4. **LLM Extraction Hierarchy:**
   - Default: Local OpenAI-compatible server (`http://127.0.0.1` Ollama or `http://127.0.0.1` `llama.cpp`, `temperature=0`).
   - Opt-in: `LLM_PROVIDER=byok` (`OPENAI_BASE_URL`, `OPENAI_API_KEY`).
   - Fallback: Deterministic JSON-LD / HTML table parser when no LLM server is reachable.
5. **Security & Resource Limits:**
   - `maxConcurrency <= 5`, 5 MB `io.LimitReader` per fetch, 2s per-domain politeness delay, per-target cooldown (`watch_targets.cooldown_seconds` / `next_check_at`, default 1h), private/loopback IP SSRF blocking, constant-time Bearer token auth on `/api/v1/trigger/*`, Basic Auth / CF Access on UI routes.
   - D1 connectivity verified at collector startup before processing any targets.

---

## 3. QA/Security Remediation Summary

**Issues Fixed:**
| ID | Severity | Issue | Fix |
|----|----------|-------|-----|
| SEC-03 | Critical | `.env` contained live `CF_API_TOKEN` and default secrets on disk | Neutralized `.env` with `CHANGE_ME_*` placeholders; added committed `.env.example` |
| BUG-A | High | `summary.DurationMS` always ~0 (`time.Since(time.Now())`) | Fixed: captures `start` in `main()`, computes `time.Since(start)` |
| BUG-B | High | Nil `summary` dereference panic on `--seed-only`/`--rebuild-manifest` | Fixed: guard all `summary.*` access with `if summary != nil` |
| BUG-C | High | `runSeedOnly` was a non-functional stub | Implemented: reads `schemas/saas_seeds.json`, inserts vendors/targets into D1, rebuilds manifest |
| BUG-D | Medium | Double `err` in extractor error message (`llm=%v, deterministic=%w` with same err) | Fixed: `deterministic=%w` only |
| BUG-E | High | `handleTriggerArchive` was a stub returning zeros | Implemented: queries old snapshots, streams to S3 via `S3Archiver`, marks `archived_to_cold=1` |
| QA-1 | Low | `fmt.Printf` mixed with `slog` in `cmd/server/main.go` | Fixed: removed `fmt`, uses `slog` throughout |
| QA-2 | Low | Dockerfile `golang:1.26-alpine` vs AGENTS.md `golang:1.23-alpine` | Fixed: aligned to `golang:1.23-alpine` |
| QA-3 | Medium | Cloudflare D1 API now expects `{"statements": [...]}` wrapper, not bare array `[...]` | Fixed: `internal/storage/d1.go` wraps statements in `d1QueryRequest{Statements: ...}` |

**Remaining (non-blocking stubs):**
- `handleListVendors`, `handleListDiffs`, `handleUpsertVendor`, `handleAckDiff`, `handleLatestSnapshot` return stub responses — functional endpoints can be added later

---

## 4. Next Implementation Milestones

Phase 2 is complete. Ready for **Integration & Container Smoke Testing**:
1. Build multi-stage linux/arm64 distroless image (`docker build --platform linux/arm64`)
2. Verify `docker inspect` shows `arm64 linux`
3. Execute `collector --dry-run` inside container
4. Apply `migrations/schema.sql` + `migrations/seeds.sql` to Cloudflare D1 via wrangler
5. Deploy to SnapDeploy or Render and enable `.github/workflows/collect.yml`
