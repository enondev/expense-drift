-- Dogfood seeds: vendors & watch targets for ExpenseDrift's own stack
-- Applied via: wrangler d1 execute expensedrift --remote --file=./migrations/seeds_dogfood.sql

-- 1. Vendors (idempotent via ON CONFLICT)
INSERT INTO vendors (id, name, category, homepage_url, status, created_at) VALUES
  ('digitalocean', 'Digital Ocean', 'cloud_hosting', 'https://www.digitalocean.com', 'active', strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('render', 'Render', 'cloud_hosting', 'https://render.com', 'active', strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('snapdeploy', 'SnapDeploy', 'cloud_hosting', 'https://snapdeploy.dev', 'active', strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('kilocode', 'KiloCode', 'ai_infra', 'https://kilo.ai', 'active', strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('nvidia-nim', 'NVIDIA NIM', 'ai_infra', 'https://build.nvidia.com', 'active', strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('openrouter', 'OpenRouter', 'ai_infra', 'https://openrouter.ai', 'active', strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('google-workspace', 'Google Workspace', 'saas_productivity', 'https://workspace.google.com', 'active', strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('tailscale', 'Tailscale', 'networking', 'https://tailscale.com', 'active', strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('belmo', 'Belmo', 'cloud_hosting', 'https://belmo.io', 'active', strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
ON CONFLICT(id) DO UPDATE SET
  name = excluded.name,
  category = excluded.category,
  homepage_url = excluded.homepage_url;

-- 2. Watch targets (idempotent via ON CONFLICT on the unique (vendor_id, url) constraint)
INSERT INTO watch_targets (id, vendor_id, surface_type, url, extract_selector, fetch_tier, last_raw_hash, last_semantic_hash, last_composite_hash, last_checked_at, consecutive_failures, cooldown_seconds, next_check_at) VALUES
  -- Digital Ocean
  ('digitalocean:pricing', 'digitalocean', 'pricing', 'https://www.digitalocean.com/pricing', NULL, 1, NULL, NULL, NULL, NULL, 0, 4500, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('digitalocean:rate-limits', 'digitalocean', 'rate_limits', 'https://docs.digitalocean.com/platform/resource-limits/', NULL, 1, NULL, NULL, NULL, NULL, 0, 4500, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('digitalocean:tos-dpa', 'digitalocean', 'tos_dpa', 'https://www.digitalocean.com/legal/terms-of-service-agreement', NULL, 1, NULL, NULL, NULL, NULL, 0, 4500, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  -- Render
  ('render:pricing', 'render', 'pricing', 'https://render.com/pricing', NULL, 1, NULL, NULL, NULL, NULL, 0, 4500, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('render:rate-limits', 'render', 'rate_limits', 'https://api-docs.render.com/reference/rate-limiting', NULL, 1, NULL, NULL, NULL, NULL, 0, 4500, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('render:tos-dpa', 'render', 'tos_dpa', 'https://render.com/terms', NULL, 1, NULL, NULL, NULL, NULL, 0, 4500, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  -- SnapDeploy
  ('snapdeploy:pricing', 'snapdeploy', 'pricing', 'https://snapdeploy.dev/pricing', NULL, 1, NULL, NULL, NULL, NULL, 0, 4500, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('snapdeploy:rate-limits', 'snapdeploy', 'rate_limits', 'https://snapdeploy.dev/api', NULL, 1, NULL, NULL, NULL, NULL, 0, 4500, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('snapdeploy:tos-dpa', 'snapdeploy', 'tos_dpa', 'https://snapdeploy.dev/terms', NULL, 1, NULL, NULL, NULL, NULL, 0, 4500, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  -- KiloCode
  ('kilocode:pricing', 'kilocode', 'pricing', 'https://kilo.ai/pricing', NULL, 1, NULL, NULL, NULL, NULL, 0, 4500, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('kilocode:rate-limits', 'kilocode', 'rate_limits', 'https://kilo.ai/docs/getting-started/rate-limits-and-costs', NULL, 1, NULL, NULL, NULL, NULL, 0, 4500, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('kilocode:tos-dpa', 'kilocode', 'tos_dpa', 'https://kilo.ai/terms', NULL, 1, NULL, NULL, NULL, NULL, 0, 4500, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  -- NVIDIA NIM
  ('nvidia-nim:pricing', 'nvidia-nim', 'pricing', 'https://build.nvidia.com/', NULL, 1, NULL, NULL, NULL, NULL, 0, 4500, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('nvidia-nim:rate-limits', 'nvidia-nim', 'rate_limits', 'https://build.nvidia.com/', NULL, 1, NULL, NULL, NULL, NULL, 0, 4500, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  -- OpenRouter
  ('openrouter:pricing', 'openrouter', 'pricing', 'https://openrouter.ai/pricing', NULL, 1, NULL, NULL, NULL, NULL, 0, 4500, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('openrouter:rate-limits', 'openrouter', 'rate_limits', 'https://openrouter.ai/docs/api_reference/limits', NULL, 1, NULL, NULL, NULL, NULL, 0, 4500, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('openrouter:tos-dpa', 'openrouter', 'tos_dpa', 'https://openrouter.ai/terms', NULL, 1, NULL, NULL, NULL, NULL, 0, 4500, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  -- Google Workspace (includes Gmail)
  ('google-workspace:pricing', 'google-workspace', 'pricing', 'https://workspace.google.com/pricing', NULL, 1, NULL, NULL, NULL, NULL, 0, 4500, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('google-workspace:rate-limits', 'google-workspace', 'rate_limits', 'https://developers.google.com/workspace/guides/quota-overview', NULL, 1, NULL, NULL, NULL, NULL, 0, 4500, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('google-workspace:tos-dpa', 'google-workspace', 'tos_dpa', 'https://workspace.google.com/terms/service-terms/', NULL, 1, NULL, NULL, NULL, NULL, 0, 4500, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  -- Tailscale
  ('tailscale:pricing', 'tailscale', 'pricing', 'https://tailscale.com/pricing', NULL, 1, NULL, NULL, NULL, NULL, 0, 4500, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('tailscale:tos-dpa', 'tailscale', 'tos_dpa', 'https://tailscale.com/terms', NULL, 1, NULL, NULL, NULL, NULL, 0, 4500, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  -- Belmo
  ('belmo:pricing', 'belmo', 'pricing', 'https://belmo.io/pricing', NULL, 1, NULL, NULL, NULL, NULL, 0, 4500, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('belmo:tos-dpa', 'belmo', 'tos_dpa', 'https://belmo.io/terms', NULL, 1, NULL, NULL, NULL, NULL, 0, 4500, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
ON CONFLICT(vendor_id, url) DO UPDATE SET
  id = excluded.id,
  surface_type = excluded.surface_type,
  extract_selector = excluded.extract_selector,
  fetch_tier = excluded.fetch_tier,
  next_check_at = excluded.next_check_at;