package differ

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"expensedrift/internal/model"
)

var leadingNumber = regexp.MustCompile(`^\s*(\d+(?:\.\d+)?)`)

// Compare detects commercial shadow inflation between two BillingRuleSets,
// classifying drift type, assigning severity (1-5), and generating RFC 6902 patches.
func Compare(prev, curr *model.BillingRuleSet) (model.BillingDiffResult, bool) {
	p := cloneRuleSet(prev)
	c := cloneRuleSet(curr)
	p.Normalize()
	c.Normalize()

	var patches []model.JSONPatchOp
	var findings []string
	maxSeverity := 0
	var primaryDrift model.DriftType

	record := func(drift model.DriftType, sev int, msg string, op model.JSONPatchOp) {
		patches = append(patches, op)
		findings = append(findings, msg)
		if sev > maxSeverity {
			maxSeverity = sev
			primaryDrift = drift
		}
	}

	prevTiers := make(map[string]model.PricingTier, len(p.Tiers))
	for _, t := range p.Tiers {
		prevTiers[t.TierName] = t
	}
	currTiers := make(map[string]model.PricingTier, len(c.Tiers))
	for _, t := range c.Tiers {
		currTiers[t.TierName] = t
	}

	// Deleted tiers (free tier removed -> upmarket push).
	// Iterate by index so RFC 6902 array paths stay numeric.
	for idx, pt := range p.Tiers {
		if _, exists := currTiers[pt.TierName]; !exists {
			sev := 4
			if pt.BasePriceMonthly == nil || *pt.BasePriceMonthly == 0 {
				sev = 5
			}
			record(model.DriftFeatureGated, sev,
				fmt.Sprintf("tier %q removed (pricing pushed upmarket)", pt.TierName),
				model.JSONPatchOp{Op: "remove", Path: fmt.Sprintf("/tiers/%d", idx)},
			)
		}
	}

	for idx, ct := range c.Tiers {
		pt, exists := prevTiers[ct.TierName]
		basePath := fmt.Sprintf("/tiers/%d", idx)
		if !exists {
			record(model.DriftFeatureGated, 2, fmt.Sprintf("new tier %q introduced", ct.TierName), model.JSONPatchOp{
				Op: "add", Path: basePath, Value: ct,
			})
			continue
		}

		// 1. Base monthly price hike (directional: ignores price drops)
		if pt.BasePriceMonthly != nil && ct.BasePriceMonthly != nil && *ct.BasePriceMonthly > *pt.BasePriceMonthly {
			pct := 0.0
			if *pt.BasePriceMonthly > 0 {
				pct = ((*ct.BasePriceMonthly - *pt.BasePriceMonthly) / *pt.BasePriceMonthly) * 100
			}
			sev := 3
			if pct >= 20 || *pt.BasePriceMonthly == 0 {
				sev = 5
			}
			record(model.DriftPriceHike, sev,
				fmt.Sprintf("tier %q price increased from %.2f to %.2f %s", ct.TierName, *pt.BasePriceMonthly, *ct.BasePriceMonthly, c.Currency),
				model.JSONPatchOp{Op: "replace", Path: basePath + "/base_price_monthly", Value: *ct.BasePriceMonthly},
			)
		}

		// 2. Annual discount reduction / removal
		discCut := false
		if pt.AnnualDiscountPct != nil && *pt.AnnualDiscountPct > 0 {
			if ct.AnnualDiscountPct == nil || *ct.AnnualDiscountPct < *pt.AnnualDiscountPct {
				discCut = true
			}
		}
		if discCut {
			record(model.DriftPriceHike, 3,
				fmt.Sprintf("tier %q annual discount reduced or removed", ct.TierName),
				model.JSONPatchOp{Op: "replace", Path: basePath + "/annual_discount_pct", Value: ct.AnnualDiscountPct},
			)
		}

		// 3. Credit multiplier or metering unit shift
		if pt.MeteringUnit != ct.MeteringUnit {
			record(model.DriftCreditMultiplier, 4,
				fmt.Sprintf("tier %q metering changed from %q to %q", ct.TierName, pt.MeteringUnit, ct.MeteringUnit),
				model.JSONPatchOp{Op: "replace", Path: basePath + "/metering_unit", Value: ct.MeteringUnit},
			)
		}
		if ct.CreditMultiplier != nil && (pt.CreditMultiplier == nil || *ct.CreditMultiplier > *pt.CreditMultiplier) {
			oldMult := 1.0
			if pt.CreditMultiplier != nil {
				oldMult = *pt.CreditMultiplier
			}
			record(model.DriftCreditMultiplier, 4,
				fmt.Sprintf("tier %q credit multiplier rose from %.2fx to %.2fx", ct.TierName, oldMult, *ct.CreditMultiplier),
				model.JSONPatchOp{Op: "replace", Path: basePath + "/credit_multiplier", Value: *ct.CreditMultiplier},
			)
		}

		// 4. Hidden seat minimum increase
		if ct.SeatMinimum > pt.SeatMinimum {
			record(model.DriftSeatMinimum, 4,
				fmt.Sprintf("tier %q seat minimum increased from %d to %d", ct.TierName, pt.SeatMinimum, ct.SeatMinimum),
				model.JSONPatchOp{Op: "replace", Path: basePath + "/seat_minimum", Value: ct.SeatMinimum},
			)
		}

		// 5. Feature removed from tier (pushed upmarket) - RFC 6902 numeric index paths
		prevFeatIndex := make(map[string]int, len(pt.GatedFeatures))
		for i, f := range pt.GatedFeatures {
			prevFeatIndex[f] = i
		}
		for _, oldFeat := range pt.GatedFeatures {
			if !slices.Contains(ct.GatedFeatures, oldFeat) {
				record(model.DriftFeatureGated, 4,
					fmt.Sprintf("feature %q removed from tier %q", oldFeat, ct.TierName),
					model.JSONPatchOp{Op: "remove", Path: fmt.Sprintf("%s/gated_features/%d", basePath, prevFeatIndex[oldFeat])},
				)
			}
		}
	}

	// 6. Rate limit / quota changes - skip increases and "unlimited"
	prevLimits := make(map[string]string, len(p.RateLimits))
	for _, r := range p.RateLimits {
		prevLimits[r.TierName+":"+r.MetricName] = r.LimitValue
	}
	for idx, cr := range c.RateLimits {
		key := cr.TierName + ":" + cr.MetricName
		oldVal, ok := prevLimits[key]
		if !ok {
			continue
		}
		if oldVal == cr.LimitValue {
			continue
		}
		if cr.LimitValue == "unlimited" {
			continue
		}
		oldNum, oldUnit := parseLimit(oldVal)
		newNum, newUnit := parseLimit(cr.LimitValue)
		quotaCut := false
		if oldNum >= 0 && newNum >= 0 && oldUnit == newUnit && newNum < oldNum {
			quotaCut = true
		} else if oldNum < 0 || newNum < 0 {
			// Fall back to string compare for non-numeric values.
			quotaCut = true
		}
		if !quotaCut {
			continue
		}
		record(model.DriftQuotaCut, 3,
			fmt.Sprintf("quota %q on tier %q changed from %q to %q", cr.MetricName, cr.TierName, oldVal, cr.LimitValue),
			model.JSONPatchOp{Op: "replace", Path: fmt.Sprintf("/rate_limits/%d/limit_value", idx), Value: cr.LimitValue},
		)
	}

	// 7. Contract / TOS renewal traps
	if c.ContractTerms.AutoRenewalNoticeDays != nil {
		oldDays := 0
		if p.ContractTerms.AutoRenewalNoticeDays != nil {
			oldDays = *p.ContractTerms.AutoRenewalNoticeDays
		}
		if *c.ContractTerms.AutoRenewalNoticeDays > oldDays {
			record(model.DriftTOSTrap, 4,
				fmt.Sprintf("auto-renewal notice window extended from %d to %d days", oldDays, *c.ContractTerms.AutoRenewalNoticeDays),
				model.JSONPatchOp{Op: "replace", Path: "/contract_terms/auto_renewal_notice_days", Value: *c.ContractTerms.AutoRenewalNoticeDays},
			)
		}
	}
	if p.ContractTerms.PriceLockClause && !c.ContractTerms.PriceLockClause {
		record(model.DriftTOSTrap, 5,
			"price-lock clause removed from contract terms",
			model.JSONPatchOp{Op: "replace", Path: "/contract_terms/price_lock_clause", Value: false},
		)
	}
	if p.ContractTerms.OverageBehavior == model.OverageHardCap && c.ContractTerms.OverageBehavior == model.OverageAutoBill {
		record(model.DriftTOSTrap, 4,
			"overage behavior shifted from hard_cap to auto_bill",
			model.JSONPatchOp{Op: "replace", Path: "/contract_terms/overage_behavior", Value: string(model.OverageAutoBill)},
		)
	}

	if len(patches) == 0 {
		return model.BillingDiffResult{}, false
	}

	return model.BillingDiffResult{
		DriftType: primaryDrift,
		Severity:  maxSeverity,
		Summary:   strings.Join(findings, "; "),
		PatchOps:  patches,
	}, true
}

// cloneRuleSet deep-clones slices so caller inputs remain immutable after Normalize().
func cloneRuleSet(src *model.BillingRuleSet) model.BillingRuleSet {
	dst := *src
	if src.Tiers != nil {
		dst.Tiers = make([]model.PricingTier, len(src.Tiers))
		for i, t := range src.Tiers {
			dst.Tiers[i] = t
			dst.Tiers[i].GatedFeatures = slices.Clone(t.GatedFeatures)
		}
	}
	if src.RateLimits != nil {
		dst.RateLimits = slices.Clone(src.RateLimits)
	}
	return dst
}

// parseLimit returns the leading numeric value and unit token from a limit string,
// e.g. "120/min" -> (120, "min"), "unlimited" -> (-1, "").
func parseLimit(v string) (float64, string) {
	m := leadingNumber.FindStringSubmatch(v)
	if len(m) < 2 {
		return -1, ""
	}
	num, _ := strconv.ParseFloat(m[1], 64)
	rest := strings.TrimSpace(v[len(m[1]):])
	unit := strings.TrimLeft(rest, "/:")
	unit = strings.TrimSpace(unit)
	return num, unit
}
