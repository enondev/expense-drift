package hasher

import (
	"encoding/json"
	"testing"

	"expensedrift/internal/model"
)

func TestRawContentHashDeterministic(t *testing.T) {
	h1 := RawContentHash("raw page content")
	h2 := RawContentHash("raw page content")
	if h1 != h2 {
		t.Fatal("raw content hash must be deterministic")
	}
	if h1 == RawContentHash("different content") {
		t.Fatal("different content must produce different hash")
	}
}

func TestSemanticSchemaHashDeterministicAndSortInsensitive(t *testing.T) {
	a := &model.BillingRuleSet{
		VendorID: "vercel",
		SurfaceType: model.SurfacePricing,
		Currency: "USD",
		Tiers: []model.PricingTier{
			{TierName: "pro", SeatMinimum: 2, GatedFeatures: []string{"sso", "audit_logs"}},
			{TierName: "free", SeatMinimum: 1, GatedFeatures: []string{}},
		},
		RateLimits: []model.QuotaRule{
			{MetricName: "api_requests_per_min", TierName: "pro", LimitValue: "120/min"},
			{MetricName: "api_requests_per_month", TierName: "free", LimitValue: "1000/month"},
		},
		ContractTerms: model.ContractTerms{PriceLockClause: true, OverageBehavior: model.OverageHardCap},
	}
	b := &model.BillingRuleSet{
		VendorID: "vercel",
		SurfaceType: model.SurfacePricing,
		Currency: "USD",
		Tiers: []model.PricingTier{
			{TierName: "free", SeatMinimum: 1, GatedFeatures: []string{}},
			{TierName: "pro", SeatMinimum: 2, GatedFeatures: []string{"audit_logs", "sso"}},
		},
		RateLimits: []model.QuotaRule{
			{MetricName: "api_requests_per_month", TierName: "free", LimitValue: "1000/month"},
			{MetricName: "api_requests_per_min", TierName: "pro", LimitValue: "120/min"},
		},
		ContractTerms: model.ContractTerms{PriceLockClause: true, OverageBehavior: model.OverageHardCap},
	}

	ha, err := SemanticSchemaHash(a)
	if err != nil {
		t.Fatalf("semantic hash a: %v", err)
	}
	hb, err := SemanticSchemaHash(b)
	if err != nil {
		t.Fatalf("semantic hash b: %v", err)
	}
	if ha != hb {
		t.Fatal("semantic hash must be order-insensitive after normalization")
	}

	// Change a field -> hash must change.
	c := clone(a)
	c.Tiers[0].SeatMinimum = 9
	hc, err := SemanticSchemaHash(c)
	if err != nil {
		t.Fatalf("semantic hash c: %v", err)
	}
	if ha == hc {
		t.Fatal("semantic hash must change when a field changes")
	}
}

func TestCompositeAuditHashChainsPrev(t *testing.T) {
	first := CompositeAuditHash("vercel", "", "raw", "sem", "2026-01-01T00:00:00Z")
	second := CompositeAuditHash("vercel", first, "raw", "sem", "2026-01-02T00:00:00Z")
	if first == second {
		t.Fatal("composite hash must change when prev_composite_hash or timestamp changes")
	}
	// Same inputs -> same output.
	again := CompositeAuditHash("vercel", first, "raw", "sem", "2026-01-02T00:00:00Z")
	if second != again {
		t.Fatal("composite hash must be deterministic")
	}
}

func TestCascadeReturnsAllThree(t *testing.T) {
	creditMult := 1.5
	r := &model.BillingRuleSet{
		VendorID: "vercel",
		SurfaceType: model.SurfacePricing,
		Currency: "USD",
		Tiers: []model.PricingTier{{TierName: "pro", BasePriceMonthly: &creditMult, SeatMinimum: 1}},
		RateLimits: []model.QuotaRule{{MetricName: "api_requests_per_min", TierName: "pro", LimitValue: "120/min"}},
		ContractTerms: model.ContractTerms{PriceLockClause: false, OverageBehavior: model.OverageUnknown},
	}
	raw, sem, comp, err := Cascade("vercel", "", "raw page", "2026-01-01T00:00:00Z", r)
	if err != nil {
		t.Fatalf("cascade: %v", err)
	}
	if raw == "" || sem == "" || comp == "" {
		t.Fatal("all three hashes must be non-empty")
	}
	if raw == sem || sem == comp {
		t.Fatal("raw, semantic, and composite hashes must differ")
	}
}

func TestValidateCascadeRecomputation(t *testing.T) {
	creditMult := 1.5
	r := &model.BillingRuleSet{
		VendorID: "vercel",
		SurfaceType: model.SurfacePricing,
		Currency: "USD",
		Tiers: []model.PricingTier{{TierName: "pro", BasePriceMonthly: &creditMult, SeatMinimum: 1}},
		RateLimits: []model.QuotaRule{{MetricName: "api_requests_per_min", TierName: "pro", LimitValue: "120/min"}},
		ContractTerms: model.ContractTerms{PriceLockClause: false, OverageBehavior: model.OverageUnknown},
	}
	raw, sem, comp, err := Cascade("vercel", "prev", "raw page", "2026-01-01T00:00:00Z", r)
	if err != nil {
		t.Fatalf("cascade: %v", err)
	}
	ok, err := ValidateCascadeRecomputation("vercel", "prev", "raw page", "2026-01-01T00:00:00Z", raw, sem, comp, r)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !ok {
		t.Fatal("recomputed cascade must match stored hashes")
	}
	// Tampered stored hash -> invalid.
	ok, _ = ValidateCascadeRecomputation("vercel", "prev", "raw page", "2026-01-01T00:00:00Z", "tampered", sem, comp, r)
	if ok {
		t.Fatal("tampered raw hash must invalidate cascade")
	}
}

func TestCanonicalJSONIsStable(t *testing.T) {
	r := &model.BillingRuleSet{
		VendorID: "x",
		SurfaceType: model.SurfaceRateLimits,
		Tiers: []model.PricingTier{{TierName: "b", SeatMinimum: 2, GatedFeatures: []string{"z", "a"}}, {TierName: "a", SeatMinimum: 1}},
		ContractTerms: model.ContractTerms{OverageBehavior: model.OverageThrottle},
	}
	b1, err := CanonicalJSON(r)
	if err != nil {
		t.Fatalf("canonical json: %v", err)
	}
	b2, err := CanonicalJSON(r)
	if err != nil {
		t.Fatalf("canonical json: %v", err)
	}
	if string(b1) != string(b2) {
		t.Fatal("canonical JSON must be stable across calls")
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(b1, &parsed); err != nil {
		t.Fatalf("canonical output must be valid JSON: %v", err)
	}
	if parsed["vendor_id"] != "x" {
		t.Fatal("canonical JSON must preserve fields")
	}
}

func clone(r *model.BillingRuleSet) *model.BillingRuleSet {
	b, _ := json.Marshal(r)
	var out model.BillingRuleSet
	json.Unmarshal(b, &out)
	return &out
}
