package extractor

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"expensedrift/internal/model"
)

var pricePattern = regexp.MustCompile(`(?i)\$\s*(\d{1,3}(?:,\d{3})*(?:\.\d+)?)`)
var tierPattern = regexp.MustCompile(`(?i)(free|starter|pro|growth|business|enterprise)\b`)
var annualPattern = regexp.MustCompile(`(?i)(/yr|/year|/annum|annually|per year|per annum)`)

// extractorJSONLDObject returns the first product/offer JSON-LD object.
func extractorJSONLDObject(jsonLD string) map[string]interface{} {
	blocks := []string{jsonLD}
	for _, block := range blocks {
		var arr []map[string]interface{}
		if err := json.Unmarshal([]byte(block), &arr); err == nil && len(arr) > 0 {
			for _, m := range arr {
				if isOfferOrProduct(m) {
					return m
				}
			}
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(block), &m); err == nil {
			if isOfferOrProduct(m) {
				return m
			}
		}
	}
	return nil
}

func isOfferOrProduct(m map[string]interface{}) bool {
	t, _ := m["@type"].(string)
	return t == "Offer" || t == "Product" || t == "AggregateOffer"
}

// parsePrice accepts float64 or JSON-string numbers, trimming $ and commas.
func parsePrice(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case string:
		s := strings.TrimSpace(n)
		s = strings.TrimPrefix(s, "$")
		s = strings.ReplaceAll(s, ",", "")
		f, err := strconv.ParseFloat(s, 64)
		return f, err == nil
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func populateFromJSONLD(ruleset *model.BillingRuleSet, obj map[string]interface{}) {
	if name, ok := obj["name"].(string); ok && name != "" {
		ruleset.VendorID = slugify(name)
	}
	if price, ok := parsePrice(obj["price"]); ok {
		tier := model.PricingTier{
			TierName:           "standard",
			BasePriceMonthly:   &price,
			MeteringUnit:       model.MeteringFlat,
			SeatMinimum:        model.DefaultSeatMinimum,
			GatedFeatures:      []string{},
		}
		ruleset.Tiers = []model.PricingTier{tier}
	}
	if currency, ok := obj["priceCurrency"].(string); ok {
		ruleset.Currency = strings.ToUpper(currency)
	}
}

// populateFromText parses distilled text for $ price patterns as a last resort.
// Uses exact byte offsets so repeated prices map to the correct tier context.
func populateFromText(ruleset *model.BillingRuleSet, text string) {
	seen := map[string]bool{}
	// Pre-compute tier keyword positions so each price maps to the nearest tier before it.
	tierMatches := tierPattern.FindAllStringIndex(text, -1)

	for _, m := range pricePattern.FindAllStringSubmatchIndex(text, -1) {
		if len(m) < 2 {
			continue
		}
		matchStart, matchEnd := m[0], m[1]
		priceStr := text[matchStart:matchEnd]
		// Strip leading $ and commas for parsing.
		numStr := strings.TrimPrefix(priceStr, "$")
		numStr = strings.ReplaceAll(numStr, ",", "")
		price, err := strconv.ParseFloat(numStr, 64)
		if err != nil {
			continue
		}

		// Detect annual pricing in trailing context and normalize to monthly.
		tailStart := matchEnd
		tailEnd := matchEnd + 25
		if tailEnd > len(text) {
			tailEnd = len(text)
		}
		if annualPattern.MatchString(text[tailStart:tailEnd]) {
			price = price / 12
		}

		// Find the closest tier keyword ending before this price starts.
		tierName := "tier"
		for _, tm := range tierMatches {
			if tm[1] <= matchStart {
				tierName = strings.ToLower(text[tm[0]:tm[1]])
			} else {
				break
			}
		}
		if seen[tierName] {
			continue
		}
		seen[tierName] = true
		ruleset.Tiers = append(ruleset.Tiers, model.PricingTier{
			TierName:           tierName,
			BasePriceMonthly:   &price,
			MeteringUnit:       model.MeteringFlat,
			SeatMinimum:        model.DefaultSeatMinimum,
			GatedFeatures:      []string{},
		})
	}
}

func slugify(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "&", "and")
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Ensure fmt is used.
var _ = fmt.Sprintf
