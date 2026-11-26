PRAGMA foreign_keys = ON;

-- 1. Tracked SaaS Vendors
CREATE TABLE IF NOT EXISTS vendors (
    id TEXT PRIMARY KEY,                  -- e.g., "linear", "vercel", "openai"
    name TEXT NOT NULL,
    category TEXT NOT NULL,               -- "ai_infra", "devtools", "crm", "cloud"
    homepage_url TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',-- 'active' | 'paused'
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

-- 2. Multi-Surface Watch Targets (Pricing, Docs/Rate-Limits, Legal/TOS, Sitemap)
CREATE TABLE IF NOT EXISTS watch_targets (
    id TEXT PRIMARY KEY,                  -- e.g., "vercel:pricing", "vercel:rate-limits"
    vendor_id TEXT NOT NULL REFERENCES vendors(id) ON DELETE CASCADE,
    surface_type TEXT NOT NULL,           -- 'pricing' | 'rate_limits' | 'tos_dpa' | 'sitemap' | 'openapi'
    url TEXT NOT NULL,
    extract_selector TEXT,                -- Optional CSS selector or JSONPath hint
    fetch_tier INTEGER NOT NULL DEFAULT 1,-- 1=HTTP/JSON, 2=DOM, 3=External Render Fallback
    last_raw_hash TEXT,
    last_semantic_hash TEXT,
    last_composite_hash TEXT,
    last_checked_at TEXT,
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    cooldown_seconds INTEGER NOT NULL DEFAULT 4500,-- Seconds to wait before re-checking (default 7.5min)
    next_check_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),-- Next allowed check time
    UNIQUE(vendor_id, url)
);
CREATE INDEX IF NOT EXISTS idx_targets_vendor ON watch_targets(vendor_id);

-- 3. Hot Semantic Snapshots (Retained 30 days in D1, then offloaded to B2/Tigris Parquet)
CREATE TABLE IF NOT EXISTS snapshots (
    id TEXT PRIMARY KEY,                  -- ULID
    target_id TEXT NOT NULL REFERENCES watch_targets(id) ON DELETE CASCADE,
    vendor_id TEXT NOT NULL,
    raw_content_hash TEXT NOT NULL,
    semantic_schema_hash TEXT NOT NULL,
    composite_audit_hash TEXT NOT NULL UNIQUE,
    prev_composite_hash TEXT,
    canonical_json TEXT NOT NULL,         -- Normalized BillingRuleSet JSON
    extractor_model TEXT NOT NULL,        -- e.g., "ollama/qwen2.5:7b" or "byok/gpt-4o-mini" or "deterministic"
    archived_to_cold INTEGER NOT NULL DEFAULT 0,
    captured_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_snapshots_target_time ON snapshots(target_id, captured_at DESC);
CREATE INDEX IF NOT EXISTS idx_snapshots_cold_queue ON snapshots(archived_to_cold, captured_at);

-- 4. Detected Shadow Inflation & Billing Rule Diffs
CREATE TABLE IF NOT EXISTS billing_diffs (
    id TEXT PRIMARY KEY,                  -- ULID
    vendor_id TEXT NOT NULL REFERENCES vendors(id) ON DELETE CASCADE,
    target_id TEXT NOT NULL REFERENCES watch_targets(id) ON DELETE CASCADE,
    from_snapshot_id TEXT REFERENCES snapshots(id),
    to_snapshot_id TEXT NOT NULL REFERENCES snapshots(id),
    drift_type TEXT NOT NULL,             -- 'price_hike' | 'credit_multiplier' | 'seat_minimum' | 'quota_cut' | 'feature_gated' | 'tos_trap'
    severity INTEGER NOT NULL,            -- 1 (info) to 5 (critical renewal impact)
    summary TEXT NOT NULL,                -- Human-readable diff summary
    diff_patch_json TEXT NOT NULL,        -- RFC 6902 JSON Patch or structured field delta
    acknowledged INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_diffs_severity_time ON billing_diffs(acknowledged, severity DESC, created_at DESC);