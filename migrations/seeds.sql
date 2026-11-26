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
INSERT INTO watch_targets (id, vendor_id, surface_type, url, extract_selector, fetch_tier) VALUES
  ('vercel:pricing', 'vercel', 'pricing', 'https://vercel.com/pricing', NULL, 1),
  ('vercel:rate-limits', 'vercel', 'rate_limits', 'https://vercel.com/docs/limits', NULL, 1),
  ('linear:pricing', 'linear', 'pricing', 'https://linear.app/pricing', NULL, 1),
  ('linear:tos-dpa', 'linear', 'tos_dpa', 'https://linear.app/terms', NULL, 1),
  ('supabase:pricing', 'supabase', 'pricing', 'https://supabase.com/pricing', NULL, 1),
  ('supabase:rate-limits', 'supabase', 'rate_limits', 'https://supabase.com/docs/guides/platform/limits', NULL, 1),
  ('openai:pricing', 'openai', 'pricing', 'https://openai.com/api/pricing/', NULL, 1),
  ('openai:rate-limits', 'openai', 'rate_limits', 'https://platform.openai.com/docs/guides/rate-limits', NULL, 1),
  ('anthropic:pricing', 'anthropic', 'pricing', 'https://www.anthropic.com/pricing', NULL, 1),
  ('anthropic:rate-limits', 'anthropic', 'rate_limits', 'https://docs.anthropic.com/en/api/rate-limits', NULL, 1),
  ('cloudflare:pricing', 'cloudflare', 'pricing', 'https://developers.cloudflare.com/workers/platform/pricing/', NULL, 1),
  ('cloudflare:rate-limits', 'cloudflare', 'rate_limits', 'https://developers.cloudflare.com/workers/platform/limits/', NULL, 1),
  ('github:pricing', 'github', 'pricing', 'https://github.com/pricing', NULL, 1),
  ('github:rate-limits', 'github', 'rate_limits', 'https://docs.github.com/en/rest/overview/resources-in-the-rest-api#rate-limiting', NULL, 1),
  ('stripe:pricing', 'stripe', 'pricing', 'https://stripe.com/pricing', NULL, 1),
  ('stripe:tos-dpa', 'stripe', 'tos_dpa', 'https://stripe.com/legal/ssa', NULL, 1)
ON CONFLICT(id) DO UPDATE SET
  vendor_id = excluded.vendor_id,
  surface_type = excluded.surface_type,
  url = excluded.url,
  extract_selector = excluded.extract_selector,
  fetch_tier = excluded.fetch_tier;
