package extractor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"expensedrift/internal/model"
)

func mockLLMServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(405)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = b
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		io.WriteString(w, body)
	}))
}

func TestExtractorLLMSuccess(t *testing.T) {
	srv := mockLLMServer(t, `{
		"id": "chatcmpl-1",
		"object": "chat.completion",
		"created": 123,
		"model": "qwen2.5:7b-instruct",
		"choices": [
			{
				"index": 0,
				"message": {
					"role": "assistant",
					"content": "{\n  \"vendor_id\": \"test-saas\",\n  \"surface_type\": \"pricing\",\n  \"currency\": \"USD\",\n  \"tiers\": [\n    {\n      \"tier_name\": \"pro\",\n      \"base_price_monthly\": 24,\n      \"annual_discount_pct\": 20,\n      \"metering_unit\": \"seat\",\n      \"credit_multiplier\": 2.5,\n      \"seat_minimum\": 5,\n      \"included_quota\": \"standard_1x_ai_2.5x_credits\",\n      \"gated_features\": [\"audit_logs\", \"sso\"]\n    }\n  ],\n  \"rate_limits\": [\n    {\"metric_name\": \"api_requests_per_min\", \"tier_name\": \"pro\", \"limit_value\": \"120/min\"}\n  ],\n  \"contract_terms\": {\n    \"auto_renewal_notice_days\": 45,\n    \"price_lock_clause\": true,\n    \"overage_behavior\": \"auto_bill\"\n  }\n}"
				}
			}
		]
	}`)
	defer srv.Close()

	e := New(&http.Client{Timeout: 10 * time.Second})
	e.provider = ProviderLocal
	e.baseURL = srv.URL
	e.model = "qwen2.5:7b-instruct"

	ruleset, modelName, err := e.Extract(context.Background(), "Pro plan $24/month", "", "")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if ruleset.VendorID != "test-saas" {
		t.Fatalf("expected vendor_id test-saas, got %q", ruleset.VendorID)
	}
	if len(ruleset.Tiers) != 1 || ruleset.Tiers[0].TierName != "pro" {
		t.Fatalf("expected pro tier, got %+v", ruleset.Tiers)
	}
	if modelName != "ollama/qwen2.5:7b-instruct" {
		t.Fatalf("expected model name ollama/..., got %q", modelName)
	}
}

func TestExtractorLLMFallsBackToDeterministic(t *testing.T) {
	srv := mockLLMServer(t, "not valid json at all")
	defer srv.Close()

	e := New(&http.Client{Timeout: 10 * time.Second})
	e.provider = ProviderLocal
	e.baseURL = srv.URL
	e.model = "qwen2.5:7b-instruct"

	ruleset, modelName, err := e.Extract(context.Background(), "Starter $9/month", "", "")
	if err != nil {
		t.Fatalf("extract should not error on fallback: %v", err)
	}
	if modelName != "deterministic" {
		t.Fatalf("expected deterministic model, got %q", modelName)
	}
	if ruleset.VendorID == "" {
		t.Fatal("expected vendor_id to be set by deterministic parser")
	}
	ruleset.Normalize()
	if ruleset.Currency != "USD" {
		t.Fatalf("expected USD currency, got %q", ruleset.Currency)
	}
}

func TestExtractorProviderOff(t *testing.T) {
	e := New(&http.Client{Timeout: 10 * time.Second})
	e.provider = ProviderOff

	ruleset, modelName, err := e.Extract(context.Background(), "Pro plan $49/month with SSO", "", "")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if modelName != "deterministic" {
		t.Fatalf("expected deterministic model, got %q", modelName)
	}
	if ruleset.VendorID == "" {
		t.Fatal("expected vendor_id to be set")
	}
}

func TestDeterministicParsesPrice(t *testing.T) {
	ruleset, err := deterministicParse("Growth plan $29/month with 5 seats minimum", "", "")
	if err != nil {
		t.Fatalf("deterministicParse: %v", err)
	}
	if len(ruleset.Tiers) == 0 {
		t.Fatal("expected at least one tier from price pattern")
	}
	if ruleset.Tiers[0].BasePriceMonthly == nil || *ruleset.Tiers[0].BasePriceMonthly != 29 {
		t.Fatalf("expected price 29, got %v", ruleset.Tiers[0].BasePriceMonthly)
	}
}

func TestStripMarkdownFences(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"```json\n{\"a\":1}\n```", "{\"a\":1}"},
		{"```\n{\"a\":1}\n```", "{\"a\":1}"},
		{"{\"a\":1}", "{\"a\":1}"},
	}
	for _, c := range cases {
		if got := stripMarkdownFences(c.in); got != c.want {
			t.Errorf("stripMarkdownFences(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParsePriceStringAndComma(t *testing.T) {
	cases := []struct {
		v      interface{}
		want   float64
		wantOk bool
	}{
		{float64(29.5), 29.5, true},
		{"20.00", 20.0, true},
		{"$20.00", 20.0, true},
		{"$1,200.00", 1200.0, true},
		{"1,200", 1200.0, true},
		{"not-a-number", 0, false},
		{nil, 0, false},
	}
	for _, c := range cases {
		got, ok := parsePrice(c.v)
		if ok != c.wantOk || got != c.want {
			t.Errorf("parsePrice(%v) = (%v, %v), want (%v, %v)", c.v, got, ok, c.want, c.wantOk)
		}
	}
}

func TestPopulateFromJSONLDStringPrice(t *testing.T) {
	obj := map[string]interface{}{
		"name":          "TestCo",
		"price":         "29.99",
		"priceCurrency": "EUR",
	}
	ruleset := &model.BillingRuleSet{
		Currency:   "USD",
		Tiers:      []model.PricingTier{},
		RateLimits: []model.QuotaRule{},
		ContractTerms: model.ContractTerms{OverageBehavior: model.OverageUnknown},
	}
	populateFromJSONLD(ruleset, obj)
	if ruleset.VendorID != "testco" {
		t.Fatalf("expected vendor testco, got %q", ruleset.VendorID)
	}
	if ruleset.Currency != "EUR" {
		t.Fatalf("expected EUR, got %q", ruleset.Currency)
	}
	if len(ruleset.Tiers) != 1 || ruleset.Tiers[0].BasePriceMonthly == nil || *ruleset.Tiers[0].BasePriceMonthly != 29.99 {
		t.Fatalf("expected price 29.99, got %v", ruleset.Tiers[0].BasePriceMonthly)
	}
}

func TestPopulateFromTextAnnualConversion(t *testing.T) {
	text := "Pro plan $120/year billed annually"
	ruleset, err := deterministicParse(text, "", "")
	if err != nil {
		t.Fatalf("deterministicParse: %v", err)
	}
	if len(ruleset.Tiers) == 0 {
		t.Fatal("expected tier from annual price")
	}
	// 120/year -> 10/month
	if ruleset.Tiers[0].BasePriceMonthly == nil || *ruleset.Tiers[0].BasePriceMonthly != 10 {
		t.Fatalf("expected monthly price 10 (120/12), got %v", ruleset.Tiers[0].BasePriceMonthly)
	}
}

func TestPopulateFromTextRepeatedPricesExactOffset(t *testing.T) {
	text := "Starter $9/month and Pro $29/month both include SSO"
	ruleset, err := deterministicParse(text, "", "")
	if err != nil {
		t.Fatalf("deterministicParse: %v", err)
	}
	if len(ruleset.Tiers) != 2 {
		t.Fatalf("expected 2 tiers, got %d", len(ruleset.Tiers))
	}
	names := map[string]bool{}
	for _, t := range ruleset.Tiers {
		names[t.TierName] = true
	}
	if !names["starter"] || !names["pro"] {
		t.Fatalf("expected starter and pro tiers, got %v", ruleset.Tiers)
	}
}
