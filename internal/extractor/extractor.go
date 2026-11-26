package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"expensedrift/internal/model"
	"expensedrift/schemas"
)

var (
	systemPrompt = schemas.LLMExtractionPrompt
	jsonSchema   = schemas.BillingRuleSetSchema
)

// Provider selects the extraction backend.
type Provider string

const (
	ProviderLocal Provider = "local"
	ProviderBYOK  Provider = "byok"
	ProviderOff   Provider = "off"
)

// LLMRequest is the OpenAI-compatible /chat/completions request.
type LLMRequest struct {
	Model       string    `json:"model"`
	Temperature float64   `json:"temperature"`
	ResponseFormat struct {
		Type       string `json:"type"`
		JSONSchema struct {
			Name   string          `json:"name"`
			Schema json.RawMessage `json:"schema"`
			Strict bool            `json:"strict"`
		} `json:"json_schema"`
	} `json:"response_format"`
	Messages []LLMMessage `json:"messages"`
}

type LLMMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// LLMResponse is the OpenAI-compatible /chat/completions response.
type LLMResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// Extractor performs LLM-backed or deterministic BillingRuleSet extraction.
type Extractor struct {
	httpClient *http.Client
	baseURL    string
	model      string
	apiKey     string
	provider   Provider
}

// New creates an Extractor from environment configuration.
func New(httpClient *http.Client) *Extractor {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	e := &Extractor{
		httpClient: httpClient,
		baseURL:    os.Getenv("LLM_BASE_URL"),
		model:      os.Getenv("LLM_MODEL"),
		apiKey:     os.Getenv("LLM_API_KEY"),
	}
	switch Provider(os.Getenv("LLM_PROVIDER")) {
	case ProviderBYOK:
		e.provider = ProviderBYOK
	case ProviderOff:
		e.provider = ProviderOff
	default:
		e.provider = ProviderLocal
		if e.baseURL == "" {
			e.baseURL = "http://127.0.0.1"
		}
		if e.model == "" {
			e.model = "qwen2.5:7b-instruct"
		}
	}
	return e
}

// Extract attempts LLM extraction first, falling back to deterministic parsing
// on any failure. Returns the normalized BillingRuleSet and the extractor model
// name used.
func (e *Extractor) Extract(ctx context.Context, distilled, nextData, jsonLD string) (*model.BillingRuleSet, string, error) {
	var ruleset *model.BillingRuleSet
	var err error

	if e.provider != ProviderOff {
		ruleset, err = e.extractLLM(ctx, distilled, nextData, jsonLD)
		if err == nil && ruleset != nil {
			ruleset.Normalize()
			return ruleset, e.modelName(), nil
		}
		// Fall through to deterministic fallback on any LLM failure.
	}

	ruleset, err = deterministicParse(distilled, nextData, jsonLD)
	if err != nil {
		return nil, "", fmt.Errorf("extraction failed: deterministic=%w", err)
	}
	ruleset.Normalize()
	return ruleset, "deterministic", nil
}

func (e *Extractor) modelName() string {
	if e.provider == ProviderBYOK {
		return "byok/" + e.model
	}
	if e.provider == ProviderLocal {
		return "ollama/" + e.model
	}
	return "deterministic"
}

func (e *Extractor) extractLLM(ctx context.Context, distilled, nextData, jsonLD string) (*model.BillingRuleSet, error) {
	endpoint := strings.TrimSuffix(e.baseURL, "/") + "/v1/chat/completions"

	userContent := fmt.Sprintf(`SYSTEM_PROMPT:
%s

JSON_SCHEMA:
%s

INPUT_CONTEXT:
%s

HYDRATION_JSON:
%s

JSON_LD:
%s

Output ONLY valid JSON matching the BillingRuleSet schema. No markdown fences, no commentary.`,
		strings.TrimSpace(systemPrompt),
		string(jsonSchema),
		distilled,
		nextData,
		jsonLD,
	)

	req := LLMRequest{
		Model:       e.model,
		Temperature: 0,
		Messages: []LLMMessage{
			{Role: "user", Content: userContent},
		},
	}
	req.ResponseFormat.Type = "json_schema"
	req.ResponseFormat.JSONSchema.Name = "BillingRuleSet"
	req.ResponseFormat.JSONSchema.Schema = jsonSchema
	req.ResponseFormat.JSONSchema.Strict = true

	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("llm request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("llm status %d: %s", resp.StatusCode, string(b))
	}

	var llmResp LLMResponse
	if err := json.NewDecoder(resp.Body).Decode(&llmResp); err != nil {
		return nil, fmt.Errorf("decode llm response: %w", err)
	}
	if len(llmResp.Choices) == 0 {
		return nil, fmt.Errorf("llm returned no choices")
	}

	content := strings.TrimSpace(llmResp.Choices[0].Message.Content)
	content = stripMarkdownFences(content)

	var ruleset model.BillingRuleSet
	if err := json.Unmarshal([]byte(content), &ruleset); err != nil {
		return nil, fmt.Errorf("parse llm json: %w", err)
	}
	if ruleset.VendorID == "" {
		return nil, fmt.Errorf("llm output missing vendor_id")
	}
	return &ruleset, nil
}

// stripMarkdownFences removes ```json ... ``` fences if the LLM wrapped output.
func stripMarkdownFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		if strings.HasPrefix(s, "json") {
			s = strings.TrimPrefix(s, "json")
		}
		s = strings.TrimSpace(s)
	}
	if strings.HasSuffix(s, "```") {
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	}
	return s
}

// deterministicParse is the zero-cost fallback JSON-LD / HTML table parser.
func deterministicParse(distilled, nextData, jsonLD string) (*model.BillingRuleSet, error) {
	ruleset := &model.BillingRuleSet{
		VendorID:   "unknown",
		SurfaceType: model.SurfacePricing,
		Currency:   model.DefaultCurrency,
		Tiers:      []model.PricingTier{},
		RateLimits: []model.QuotaRule{},
		ContractTerms: model.ContractTerms{
			OverageBehavior: model.OverageUnknown,
		},
	}

	// Prefer structured JSON-LD Offer data.
	if obj := extractorJSONLDObject(jsonLD); obj != nil {
		populateFromJSONLD(ruleset, obj)
	}

	// Fallback: parse distilled text for price patterns.
	if len(ruleset.Tiers) == 0 {
		populateFromText(ruleset, distilled)
	}

	if ruleset.VendorID == "" || ruleset.VendorID == "unknown" {
		ruleset.VendorID = "deterministic-unknown"
	}
	return ruleset, nil
}
