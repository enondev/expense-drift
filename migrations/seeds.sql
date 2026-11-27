-- Seed initial SaaS vendors and watch targets into D1.
-- Apply with: npx wrangler d1 execute expensedrift --remote --file=./migrations/seeds.sql

-- Vendors
INSERT INTO vendors (id, name, category, homepage_url, status) VALUES
  ('vercel', 'Vercel', 'cloud_hosting', 'https://vercel.com', 'active'),
  ('linear', 'Linear', 'devtools', 'https://linear.app', 'active'),
  ('supabase', 'Supabase', 'database_baas', 'https://supabase.com', 'active'),
  ('openai', 'OpenAI API', 'ai_infra', 'https://openai.com', 'active'),
  ('anthropic', 'Anthropic Claude', 'ai_infra', 'https://claude.com', 'active'),
  ('cloudflare', 'Cloudflare Workers', 'cloud_hosting', 'https://developers.cloudflare.com', 'active'),
  ('github', 'GitHub', 'devtools', 'https://github.com', 'active'),
  ('stripe', 'Stripe', 'fintech_billing', 'https://stripe.com', 'active')
ON CONFLICT(id) DO UPDATE SET
  name = excluded.name,
  category = excluded.category,
  homepage_url = excluded.homepage_url;

-- Watch Targets
INSERT INTO watch_targets (id, vendor_id, surface_type, url, extract_selector, fetch_tier, next_check_at) VALUES
  ('vercel:pricing', 'vercel', 'pricing', 'https://vercel.com/pricing', NULL, 1, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('vercel:rate-limits', 'vercel', 'rate_limits', 'https://vercel.com/docs/quotas', NULL, 1, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('linear:pricing', 'linear', 'pricing', 'https://linear.app/pricing', NULL, 1, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('linear:tos-dpa', 'linear', 'tos_dpa', 'https://linear.app/terms', NULL, 1, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('supabase:pricing', 'supabase', 'pricing', 'https://supabase.com/pricing', NULL, 1, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('supabase:rate-limits', 'supabase', 'rate_limits', 'https://supabase.com/docs/guides/platform/limits', NULL, 1, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('openai:pricing', 'openai', 'pricing', 'https://openai.com/api/pricing/', NULL, 1, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('openai:rate-limits', 'openai', 'rate_limits', 'https://platform.openai.com/docs/guides/rate-limits', NULL, 1, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('anthropic:pricing', 'anthropic', 'pricing', 'https://www.anthropic.com/pricing', NULL, 1, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('anthropic:rate-limits', 'anthropic', 'rate_limits', 'https://docs.anthropic.com/en/api/rate-limits', NULL, 1, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('cloudflare:pricing', 'cloudflare', 'pricing', 'https://developers.cloudflare.com/workers/platform/pricing/', NULL, 1, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('cloudflare:rate-limits', 'cloudflare', 'rate_limits', 'https://developers.cloudflare.com/workers/platform/limits/', NULL, 1, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('github:pricing', 'github', 'pricing', 'https://github.com/pricing', NULL, 1, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('github:rate-limits', 'github', 'rate_limits', 'https://docs.github.com/en/rest/overview/resources-in-the-rest-api#rate-limiting', NULL, 1, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('stripe:pricing', 'stripe', 'pricing', 'https://stripe.com/pricing', NULL, 1, strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
  ('stripe:tos-dpa', 'stripe', 'tos_dpa', 'https://stripe.com/legal/ssa', NULL, 1, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
ON CONFLICT(id) DO UPDATE SET
  vendor_id = excluded.vendor_id,
  surface_type = excluded.surface_type,
  url = excluded.url,
  extract_selector = excluded.extract_selector,
  fetch_tier = excluded.fetch_tier,
  next_check_at = excluded.next_check_at;
