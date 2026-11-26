package differ

import (
	"strconv"
	"strings"
	"testing"

	"expensedrift/internal/model"
)

func ptrF(v float64) *float64 { return &v }
func ptrI(v int) *int        { return &v }

func baseRuleset() *model.BillingRuleSet {
	base := 24.0
	creditMult := 2.5
	return &model.BillingRuleSet{
		VendorID:   "example-saas",
		SurfaceType: model.SurfacePricing,
		Currency:   "USD",
		Tiers: []model.PricingTier{
			{
				TierName:           "growth",
				BasePriceMonthly:   &base,
				AnnualDiscountPct:  ptrF(20),
				MeteringUnit:       model.MeteringSeat,
				CreditMultiplier:   &creditMult,
				SeatMinimum:        5,
				IncludedQuota:      "standard_1x_ai_2.5x_credits",
				GatedFeatures:      []string{"audit_logs", "sso"},
			},
			{TierName: "starter", SeatMinimum: 1, GatedFeatures: []string{}},
		},
		RateLimits: []model.QuotaRule{
			{MetricName: "api_requests_per_month", TierName: "starter", LimitValue: "1000/month"},
		},
		ContractTerms: model.ContractTerms{
			AutoRenewalNoticeDays: ptrI(45),
			PriceLockClause:       true,
			OverageBehavior:       model.OverageAutoBill,
		},
	}
}

func TestCompareNoChangesReturnsFalse(t *testing.T) {
	r := baseRuleset()
	res, changed := Compare(r, r)
	if changed {
		t.Fatal("expected no changes for identical rulesets")
	}
	if res.Summary != "" || res.Severity != 0 {
		t.Fatalf("expected empty result, got %+v", res)
	}
}

func TestComparePriceHikeOver20PercentIsCritical(t *testing.T) {
	prev := baseRuleset()
	curr := baseRuleset()
	*curr.Tiers[0].BasePriceMonthly = 30.0 // 25% hike
	res, changed := Compare(prev, curr)
	if !changed {
		t.Fatal("expected price hike to be detected")
	}
	if res.DriftType != model.DriftPriceHike {
		t.Fatalf("expected price_hike, got %s", res.DriftType)
	}
	if res.Severity != 5 {
		t.Fatalf("expected severity 5 for >=20%% hike, got %d", res.Severity)
	}
	if len(res.PatchOps) != 1 || res.PatchOps[0].Op != "replace" {
		t.Fatalf("expected single replace op, got %+v", res.PatchOps)
	}
}

func TestComparePriceHikeUnder20PercentIsModerate(t *testing.T) {
	prev := baseRuleset()
	curr := baseRuleset()
	*curr.Tiers[0].BasePriceMonthly = 27.0 // 12.5% hike
	res, changed := Compare(prev, curr)
	if !changed {
		t.Fatal("expected price hike to be detected")
	}
	if res.Severity != 3 {
		t.Fatalf("expected severity 3 for <20%% hike, got %d", res.Severity)
	}
}

func TestComparePriceDropIsIgnored(t *testing.T) {
	prev := baseRuleset()
	curr := baseRuleset()
	*curr.Tiers[0].BasePriceMonthly = 10.0 // price drop
	res, changed := Compare(prev, curr)
	if changed {
		t.Fatalf("expected price drop to be ignored, got %+v", res)
	}
}

func TestCompareNewTierDetected(t *testing.T) {
	prev := baseRuleset()
	curr := baseRuleset()
	curr.Tiers = append(curr.Tiers, model.PricingTier{TierName: "enterprise", SeatMinimum: 1})
	res, changed := Compare(prev, curr)
	if !changed {
		t.Fatal("expected new tier to be detected")
	}
	if res.DriftType != model.DriftFeatureGated {
		t.Fatalf("expected feature_gated for new tier, got %s", res.DriftType)
	}
	if res.Severity != 2 {
		t.Fatalf("expected severity 2 for new tier, got %d", res.Severity)
	}
}

func TestCompareSeatMinimumIncreaseDetected(t *testing.T) {
	prev := baseRuleset()
	curr := baseRuleset()
	curr.Tiers[0].SeatMinimum = 10
	res, changed := Compare(prev, curr)
	if !changed {
		t.Fatal("expected seat minimum increase to be detected")
	}
	if res.DriftType != model.DriftSeatMinimum {
		t.Fatalf("expected seat_minimum, got %s", res.DriftType)
	}
	if res.Severity != 4 {
		t.Fatalf("expected severity 4 for seat minimum, got %d", res.Severity)
	}
}

func TestCompareFeatureRemovedDetected(t *testing.T) {
	prev := baseRuleset()
	curr := baseRuleset()
	curr.Tiers[0].GatedFeatures = []string{"sso"} // removed audit_logs
	res, changed := Compare(prev, curr)
	if !changed {
		t.Fatal("expected feature removal to be detected")
	}
	if res.DriftType != model.DriftFeatureGated {
		t.Fatalf("expected feature_gated, got %s", res.DriftType)
	}
	if res.Severity != 4 {
		t.Fatalf("expected severity 4 for feature gated, got %d", res.Severity)
	}
}

func TestCompareQuotaChangeDetected(t *testing.T) {
	prev := baseRuleset()
	curr := baseRuleset()
	curr.RateLimits[0].LimitValue = "500/month" // quota cut
	res, changed := Compare(prev, curr)
	if !changed {
		t.Fatal("expected quota change to be detected")
	}
	if res.DriftType != model.DriftQuotaCut {
		t.Fatalf("expected quota_cut, got %s", res.DriftType)
	}
	if res.Severity != 3 {
		t.Fatalf("expected severity 3 for quota cut, got %d", res.Severity)
	}
}

func TestCompareAutoRenewalExtendedDetected(t *testing.T) {
	prev := baseRuleset()
	curr := baseRuleset()
	curr.ContractTerms.AutoRenewalNoticeDays = ptrI(60)
	res, changed := Compare(prev, curr)
	if !changed {
		t.Fatal("expected auto-renewal extension to be detected")
	}
	if res.DriftType != model.DriftTOSTrap {
		t.Fatalf("expected tos_trap, got %s", res.DriftType)
	}
	if res.Severity != 4 {
		t.Fatalf("expected severity 4 for auto-renewal, got %d", res.Severity)
	}
}

func TestComparePriceLockRemovedIsCritical(t *testing.T) {
	prev := baseRuleset()
	curr := baseRuleset()
	curr.ContractTerms.PriceLockClause = false
	res, changed := Compare(prev, curr)
	if !changed {
		t.Fatal("expected price lock removal to be detected")
	}
	if res.DriftType != model.DriftTOSTrap {
		t.Fatalf("expected tos_trap, got %s", res.DriftType)
	}
	if res.Severity != 5 {
		t.Fatalf("expected severity 5 for price lock removal, got %d", res.Severity)
	}
}

func TestCompareCreditMultiplierIncreaseDetected(t *testing.T) {
	prev := baseRuleset()
	curr := baseRuleset()
	*curr.Tiers[0].CreditMultiplier = 3.0 // was 2.5
	res, changed := Compare(prev, curr)
	if !changed {
		t.Fatal("expected credit multiplier increase to be detected")
	}
	if res.DriftType != model.DriftCreditMultiplier {
		t.Fatalf("expected credit_multiplier, got %s", res.DriftType)
	}
	if res.Severity != 4 {
		t.Fatalf("expected severity 4 for credit multiplier, got %d", res.Severity)
	}
}

func TestCompareMeteringUnitChangeDetected(t *testing.T) {
	prev := baseRuleset()
	curr := baseRuleset()
	curr.Tiers[0].MeteringUnit = model.MeteringCredit
	res, changed := Compare(prev, curr)
	if !changed {
		t.Fatal("expected metering unit change to be detected")
	}
	if res.DriftType != model.DriftCreditMultiplier {
		t.Fatalf("expected credit_multiplier, got %s", res.DriftType)
	}
	if res.Severity != 4 {
		t.Fatalf("expected severity 4 for metering change, got %d", res.Severity)
	}
}

func TestCompareDeletedTierPatchPathIsNumeric(t *testing.T) {
	prev := baseRuleset()
	curr := baseRuleset()
	// Delete the last tier by index so path is /tiers/1
	var kept []model.PricingTier
	for i, t := range curr.Tiers {
		if i != 1 {
			kept = append(kept, t)
		}
	}
	curr.Tiers = kept
	res, changed := Compare(prev, curr)
	if !changed {
		t.Fatal("expected deleted tier to be detected")
	}
	foundNumeric := false
	for _, op := range res.PatchOps {
		if op.Op == "remove" && strings.HasPrefix(op.Path, "/tiers/") {
			// Verify the path segment after /tiers/ is a pure integer.
			seg := strings.TrimPrefix(op.Path, "/tiers/")
			if _, err := strconv.Atoi(seg); err == nil {
				foundNumeric = true
			}
		}
	}
	if !foundNumeric {
		t.Fatalf("expected numeric RFC 6902 path for removed tier, got %+v", res.PatchOps)
	}
}


func TestCompareDeletedFreeTierIsCritical(t *testing.T) {
	prev := baseRuleset()
	curr := baseRuleset()
	var kept []model.PricingTier
	for _, t := range curr.Tiers {
		if t.TierName != "starter" {
			kept = append(kept, t)
		}
	}
	curr.Tiers = kept
	res, changed := Compare(prev, curr)
	if !changed {
		t.Fatal("expected deleted tier to be detected")
	}
	if res.DriftType != model.DriftFeatureGated {
		t.Fatalf("expected feature_gated, got %s", res.DriftType)
	}
	if res.Severity != 5 {
		t.Fatalf("expected severity 5 for deleted free tier, got %d", res.Severity)
	}
}

func TestCompareDeletedPaidTierIsSevere(t *testing.T) {
	prev := baseRuleset()
	curr := baseRuleset()
	var kept []model.PricingTier
	for _, t := range curr.Tiers {
		if t.TierName != "growth" {
			kept = append(kept, t)
		}
	}
	curr.Tiers = kept
	res, changed := Compare(prev, curr)
	if !changed {
		t.Fatal("expected deleted tier to be detected")
	}
	if res.Severity != 4 {
		t.Fatalf("expected severity 4 for deleted paid tier, got %d", res.Severity)
	}
}

func TestCompareAnnualDiscountCutDetected(t *testing.T) {
	prev := baseRuleset()
	curr := baseRuleset()
	curr.Tiers[0].AnnualDiscountPct = ptrF(10) // was 20
	res, changed := Compare(prev, curr)
	if !changed {
		t.Fatal("expected discount cut to be detected")
	}
	if res.DriftType != model.DriftPriceHike {
		t.Fatalf("expected price_hike, got %s", res.DriftType)
	}
}

func TestCompareOverageHardCapToAutoBillDetected(t *testing.T) {
	prev := baseRuleset()
	prev.ContractTerms.OverageBehavior = model.OverageHardCap
	curr := baseRuleset()
	curr.ContractTerms.OverageBehavior = model.OverageAutoBill
	res, changed := Compare(prev, curr)
	if !changed {
		t.Fatal("expected overage shift to be detected")
	}
	if res.DriftType != model.DriftTOSTrap {
		t.Fatalf("expected tos_trap, got %s", res.DriftType)
	}
	if res.Severity != 4 {
		t.Fatalf("expected severity 4 for overage shift, got %d", res.Severity)
	}
}

func TestCompareQuotaIncreaseIgnored(t *testing.T) {
	prev := baseRuleset()
	curr := baseRuleset()
	curr.RateLimits[0].LimitValue = "2000/month" // was 1000/month (increase)
	res, changed := Compare(prev, curr)
	if changed {
		t.Fatalf("expected quota increase to be ignored, got %+v", res)
	}
}

func TestCompareQuotaCutDetected(t *testing.T) {
	prev := baseRuleset()
	curr := baseRuleset()
	curr.RateLimits[0].LimitValue = "500/month" // was 1000/month (cut)
	res, changed := Compare(prev, curr)
	if !changed {
		t.Fatal("expected quota cut to be detected")
	}
	if res.DriftType != model.DriftQuotaCut {
		t.Fatalf("expected quota_cut, got %s", res.DriftType)
	}
}

func TestCompareUnlimitedQuotaIgnored(t *testing.T) {
	prev := baseRuleset()
	curr := baseRuleset()
	curr.RateLimits[0].LimitValue = "unlimited" // was 1000/month -> unlimited (not a cut)
	res, changed := Compare(prev, curr)
	if changed {
		t.Fatalf("expected unlimited quota to be ignored, got %+v", res)
	}
}

