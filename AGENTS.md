# AGENTS.md — ExpenseDrift Engineering Guardrails

## Project Overview
ExpenseDrift is a lightweight Go system that monitors B2B SaaS pricing pages, rate-limit docs, and terms of service for "shadow inflation" (credit multipliers, seat minimums, quota cuts, AI add-on gating).
It compiles into two binaries from one Go module:
- `cmd/collector`: Headless batch worker designed for GitHub Actions / local cron runs.
- `cmd/server`: Authenticated HTTP control plane & on-demand runner (`SnapDeploy` or `Render` free tier, `linux/arm64`, `CGO_ENABLED=0`).

## Hard Resource & Platform Constraints (DO NOT VIOLATE)
1. **Memory Ceiling (< 512 MB RAM):** Target runtime footprint MUST stay under 60 MB RSS.
   - NEVER add headless browser dependencies (Playwright, Puppeteer, chromedp, Rod).
   - NEVER buffer large HTTP responses or cold-storage exports in memory; bound `io.LimitReader` to 5 MB per page and stream cold archives via `io.Pipe`.
   - Keep `maxConcurrency <= 5` in `internal/fetcher`.
2. **Architecture & Containerization:**
   - Docker builds MUST use a **multi-stage build** (`golang:1.26-alpine` builder -> `gcr.io/distroless/static-debian12:nonroot` runtime) targeting `linux/amd64` (Render) or `linux/arm64` (SnapDeploy) with `CGO_ENABLED=0` and `-ldflags="-s -w"` to keep the final image under 25 MB.
   - Runtime container MUST run as `nonroot`, include Mozilla CA certificates for outbound TLS (scraping, D1, B2/Tigris), and bind to `0.0.0.0:$PORT` (default `8080`).
3. **Zero Paid SaaS Lock-in:**
   - Hot DB is Cloudflare D1 (SQLite dialect). Every `SELECT` query MUST filter on an indexed column to protect the 5M daily row-read cap.
   - KV usage is restricted to the single `manifest:v1` key (1 read + 1 write per batch run) to stay well within the 1,000 writes/day free limit.
   - Cold archival uses standard S3-compatible APIs (`aws-sdk-go-v2`) targeting Backblaze B2 or Tigris (no credit card required).

## Domain & Extraction Rules
1. **Local-First LLM Default:** `internal/extractor` MUST default to a local OpenAI-compatible endpoint (`http://127.0.0.1` for Ollama or `http://127.0.0.1` for `llama.cpp`) with `temperature=0` and strict JSON schema output. External providers are strictly opt-in via `LLM_PROVIDER=byok`. If no LLM endpoint responds, degrade gracefully to `deterministic.go`.
2. **Deterministic 3-Hash Cascade (`internal/hasher`):**
   - Always sort slices (`Tiers`, `GatedFeatures`, `RateLimits`) and normalize currency/units before computing `semantic_schema_hash`.
   - Never trigger a `billing_diffs` alert if only `raw_content_hash` changed while `semantic_schema_hash` remained identical.
   - Always link `prev_composite_hash` when computing `composite_audit_hash`.
3. **Polite Scraping:** Respect per-domain rate limits (minimum 2s delay between requests to the same host) and use exponential backoff with jitter on HTTP `429` and `5xx`.

## Security Requirements
- All `/api/v1/trigger/collect` and `/api/v1/trigger/archive` endpoints MUST require `Authorization: Bearer <TRIGGER_SECRET>` using `crypto/subtle.ConstantTimeCompare`.
- Human UI routes on `cmd/server` MUST enforce Basic Auth or validate Cloudflare Access JWT headers (`Cf-Access-Jwt-Assertion`).
- Validate all target URLs before fetching to block SSRF against private/loopback IP ranges (`127.0.0.0/8`, `10.0.0.0/8`, `169.254.169.254`, `172.16.0.0/12`, `192.168.0.0/16`).

## Code Style & Testing
- Keep dependencies minimal; prefer Go standard library (`net/http`, `html/template`, `crypto/sha256`, `encoding/json`) over heavy web frameworks.
- Embed web templates via `//go:embed` so `cmd/server` ships as a single self-contained binary.
- All unit tests for `internal/hasher`, `internal/differ`, and `internal/fetcher` MUST run completely offline using `net/http/httptest` fixtures.
- Run `go test -race ./...` before completing any task.

## Output Format Contract (Round-Trip Pack Protocol)
When generating or modifying repository files in chat responses, ALWAYS wrap all file outputs inside a single `<repository>` XML block matching `scripts/pack.go`:

<repository>
<file path="relative/path/to/file.go">
// complete file contents
</file>
</repository>

Rules:
- Never split files across multiple separate markdown blocks unless explicitly asked.
- Always emit complete, compilable file contents (no `// ... existing code ...` placeholders) so `go run scripts/unpack.go` can apply them atomically.