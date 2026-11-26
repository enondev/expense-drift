package model

import (
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// SurfaceType describes the class of monitored vendor artifact.
type SurfaceType string

const (
	SurfacePricing    SurfaceType = "pricing"
	SurfaceRateLimits SurfaceType = "rate_limits"
	SurfaceTOSDPA     SurfaceType = "tos_dpa"
	SurfaceSitemap    SurfaceType = "sitemap"
	SurfaceOpenAPI    SurfaceType = "openapi"
)

// MeteringUnit is the primary unit driving variable or recurring cost.
type MeteringUnit string

const (
	MeteringSeat      MeteringUnit = "seat"
	MeteringCredit    MeteringUnit = "credit"
	Metering1KTokens  MeteringUnit = "1k_tokens"
	MeteringGB        MeteringUnit = "gb"
	MeteringRequest   MeteringUnit = "request"
	MeteringFlat      MeteringUnit = "flat"
	MeteringCustom    MeteringUnit = "custom"
)

// OverageBehavior describes behavior when included quota is exceeded.
type OverageBehavior string

const (
	OverageHardCap   OverageBehavior = "hard_cap"
	OverageAutoBill  OverageBehavior = "auto_bill"
	OverageThrottle  OverageBehavior = "throttle"
	OverageUnknown   OverageBehavior = "unknown"
)

// DefaultCurrency is the ISO-4217 fallback when none is specified.
const DefaultCurrency = "USD"

// DefaultSeatMinimum is the fallback minimum seats when unspecified.
const DefaultSeatMinimum = 1

// nonceAttr matches nonce="..." or nonce='...' attribute injections.
var nonceAttr = regexp.MustCompile(`(?i)\s*nonce\s*=\s*("[^"]*"|'[^']*')`)

// csrfAttr matches common CSRF token attribute injections.
var csrfAttr = regexp.MustCompile(`(?i)\s*data-csrf\s*=\s*("[^"]*"|'[^']*')`)

// htmlComment matches HTML comment blocks.
var htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)

// collapseWhitespace matches runs of whitespace.
var collapseWhitespace = regexp.MustCompile(`\s+`)

// PricingTier is a single normalized pricing plan.
type PricingTier struct {
	TierName           string      `json:"tier_name"`
	BasePriceMonthly   *float64    `json:"base_price_monthly"`
	AnnualDiscountPct  *float64    `json:"annual_discount_pct"`
	MeteringUnit       MeteringUnit `json:"metering_unit"`
	CreditMultiplier   *float64    `json:"credit_multiplier"`
	SeatMinimum        int         `json:"seat_minimum"`
	IncludedQuota      string      `json:"included_quota"`
	GatedFeatures      []string    `json:"gated_features"`
}

// QuotaRule is a single API or usage quota ceiling.
type QuotaRule struct {
	MetricName string `json:"metric_name"`
	TierName   string `json:"tier_name"`
	LimitValue string `json:"limit_value"`
}

// ContractTerms captures auto-renewal, price-lock, and overage policy.
type ContractTerms struct {
	AutoRenewalNoticeDays *int            `json:"auto_renewal_notice_days"`
	PriceLockClause       bool            `json:"price_lock_clause"`
	OverageBehavior       OverageBehavior `json:"overage_behavior"`
}

// BillingRuleSet is the canonical normalized representation of a SaaS vendor's
// pricing, metering, quota, and contract rules.
type BillingRuleSet struct {
	VendorID      string       `json:"vendor_id"`
	SurfaceType   SurfaceType  `json:"surface_type"`
	Currency      string       `json:"currency"`
	Tiers         []PricingTier `json:"tiers"`
	RateLimits    []QuotaRule   `json:"rate_limits"`
	ContractTerms ContractTerms `json:"contract_terms"`
}

// NormalizeRaw strips nonce/csrf attributes, HTML comments, and collapses
// whitespace so dynamically injected attributes do not change the raw hash.
func NormalizeRaw(raw string) string {
	s := nonceAttr.ReplaceAllString(raw, "")
	s = csrfAttr.ReplaceAllString(s, "")
	s = htmlComment.ReplaceAllString(s, "")
	s = strings.TrimSpace(s)
	s = collapseWhitespace.ReplaceAllString(s, " ")
	return s
}

// Normalize sorts slices and applies defaults so the ruleset is deterministic
// before hashing or diffing. It mutates the receiver in place.
func (r *BillingRuleSet) Normalize() {
	if r.Currency == "" {
		r.Currency = DefaultCurrency
	}
	if r.Tiers == nil {
		r.Tiers = []PricingTier{}
	}
	if r.RateLimits == nil {
		r.RateLimits = []QuotaRule{}
	}

	slices.SortFunc(r.Tiers, func(a, b PricingTier) int {
		return strings.Compare(a.TierName, b.TierName)
	})
	slices.SortFunc(r.RateLimits, func(a, b QuotaRule) int {
		if c := strings.Compare(a.MetricName, b.MetricName); c != 0 {
			return c
		}
		return strings.Compare(a.TierName, b.TierName)
	})

	for i := range r.Tiers {
		t := &r.Tiers[i]
		if t.SeatMinimum == 0 {
			t.SeatMinimum = DefaultSeatMinimum
		}
		t.GatedFeatures = dedupeAndNormalize(t.GatedFeatures)
	}
}

// dedupeAndNormalize lowercases, trims, deduplicates, and sorts strings.
func dedupeAndNormalize(ss []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		n := strings.TrimSpace(strings.ToLower(s))
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// JSONPatchOp is a single RFC 6902 JSON Patch operation, shared across the
// differ and storage packages as a single contract.
type JSONPatchOp struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}

// Patch is an alias retained for callers that reference differ.PatchOp.
type Patch = JSONPatchOp

// DriftType classifies a detected billing-rule change.
type DriftType string

const (
	DriftPriceHike      DriftType = "price_hike"
	DriftCreditMultiplier DriftType = "credit_multiplier"
	DriftSeatMinimum    DriftType = "seat_minimum"
	DriftQuotaCut       DriftType = "quota_cut"
	DriftFeatureGated   DriftType = "feature_gated"
	DriftTOSTrap        DriftType = "tos_trap"
)

// BillingDiffResult summarizes a computed diff between two snapshots.
type BillingDiffResult struct {
	DriftType  DriftType       `json:"drift_type"`
	Severity   int             `json:"severity"`
	Summary    string          `json:"summary"`
	PatchOps   []JSONPatchOp   `json:"patch_ops"`
	FromHash   string          `json:"from_hash,omitempty"`
	ToHash     string          `json:"to_hash"`
}

// Ensure json and regexp are used.
var _ = json.Marshal
var _ = regexp.MustCompile
