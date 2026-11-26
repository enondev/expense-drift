package model

import (
	"encoding/json"
	"testing"
)

func TestNormalizeAppliesDefaultsAndSorts(t *testing.T) {
	creditMult := 2.5
	r := &BillingRuleSet{
		VendorID:   "example-saas",
		SurfaceType: SurfacePricing,
		Currency:   "USD",
		Tiers: []PricingTier{
			{TierName: "starter", SeatMinimum: 0, GatedFeatures: []string{"sso", "audit_logs"}},
			{TierName: "growth", BasePriceMonthly: &creditMult, SeatMinimum: 5, GatedFeatures: []string{}},
		},
		RateLimits: []QuotaRule{
			{MetricName: "api_requests_per_min", TierName: "pro", LimitValue: "120/min"},
			{MetricName: "api_requests_per_month", TierName: "starter", LimitValue: "1000/month"},
		},
		ContractTerms: ContractTerms{PriceLockClause: false, OverageBehavior: OverageAutoBill},
	}
	r.Normalize()

	if r.Currency != "USD" {
		t.Fatalf("currency should default to USD, got %q", r.Currency)
	}
	if len(r.Tiers) != 2 || r.Tiers[0].TierName != "growth" || r.Tiers[1].TierName != "starter" {
		t.Fatalf("tiers should sort ascending by tier_name, got %v", r.Tiers)
	}
	if r.Tiers[1].SeatMinimum != 1 {
		t.Fatalf("starter seat_minimum should default to 1, got %d", r.Tiers[1].SeatMinimum)
	}
	if !sorted(r.Tiers[0].GatedFeatures) {
		t.Fatalf("gated_features should sort ascending, got %v", r.Tiers[0].GatedFeatures)
	}
	if len(r.RateLimits) != 2 || r.RateLimits[0].MetricName != "api_requests_per_min" {
		t.Fatalf("rate_limits should sort ascending by metric_name, got %v", r.RateLimits)
	}
}

func TestNormalizeEmptySlicesBecomeNonNil(t *testing.T) {
	r := &BillingRuleSet{VendorID: "x", SurfaceType: SurfacePricing}
	r.Normalize()
	if r.Tiers == nil {
		t.Error("tiers should not be nil after normalize")
	}
	if r.RateLimits == nil {
		t.Error("rate_limits should not be nil after normalize")
	}
}

func TestRoundTripJSON(t *testing.T) {
	creditMult := 1.5
	r := &BillingRuleSet{
		VendorID:      "vercel",
		SurfaceType:   SurfacePricing,
		Currency:      "USD",
		Tiers:         []PricingTier{{TierName: "pro", BasePriceMonthly: &creditMult, SeatMinimum: 1}},
		RateLimits:    []QuotaRule{{MetricName: "api_requests_per_min", TierName: "pro", LimitValue: "120/min"}},
		ContractTerms: ContractTerms{PriceLockClause: true, OverageBehavior: OverageHardCap},
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out BillingRuleSet
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.VendorID != "vercel" || out.ContractTerms.PriceLockClause != true {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}

func sorted(ss []string) bool {
	for i := 1; i < len(ss); i++ {
		if ss[i] < ss[i-1] {
			return false
		}
	}
	return true
}
