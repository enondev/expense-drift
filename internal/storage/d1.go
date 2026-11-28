package storage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// D1Client wraps the Cloudflare D1 HTTP API.
type D1Client struct {
	httpClient *http.Client
	config     Config
	baseURL    string // overrides d1QueryURL for tests
}

// NewD1Client creates a D1 client from configuration.
func NewD1Client(cfg Config) *D1Client {
	return &D1Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		config:     cfg,
	}
}

// NewD1ClientWithURL creates a D1 client pointed at a custom base URL (tests).
func NewD1ClientWithURL(cfg Config, baseURL string) *D1Client {
	return &D1Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		config:     cfg,
		baseURL:    baseURL,
	}
}

// d1QueryRequest is the Cloudflare D1 batch query request body.
// Cloudflare D1 expects {"batch": [{"sql": "...", "params": [...]}]}.
type d1QueryRequest struct {
	Batch []Statement `json:"batch"`
}

// Query executes a batch of SQL statements against the D1 database in a single
// HTTP call to minimize latency and stay under the 100k writes/day limit.
func (c *D1Client) Query(statements ...Statement) ([]Result, error) {
	if err := ValidateStatements(statements); err != nil {
		return nil, err
	}

	body, err := json.Marshal(d1QueryRequest{Batch: statements})
	if err != nil {
		return nil, fmt.Errorf("marshal d1 statements: %w", err)
	}

	url := c.baseURL
	if url == "" {
		url = d1QueryURL(c.config)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.config.CFAPIToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("d1 query request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read d1 response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("d1 query status %d: %s", resp.StatusCode, string(respBody))
	}

	var qr QueryResponse
	if err := json.Unmarshal(respBody, &qr); err != nil {
		return nil, fmt.Errorf("decode d1 response: %w", err)
	}
	if !qr.Success {
		return nil, fmt.Errorf("d1 query failed: %s", stringsJoin(qr.Errors, "; "))
	}

	results := make([]Result, 0, len(qr.Result))
	for _, r := range qr.Result {
		if !r.Success {
			return nil, fmt.Errorf("d1 statement failed: %v", r.Meta)
		}
		results = append(results, r)
	}
	return results, nil
}

// d1QueryURL builds the Cloudflare D1 query endpoint URL.
func d1QueryURL(cfg Config) string {
	return fmt.Sprintf(
		"https://api.cloudflare.com/client/v4/accounts/%s/d1/database/%s/query",
		cfg.CFAccountID, cfg.CFD1DatabaseID,
	)
}

func stringsJoin(ss []string, sep string) string {
	var b bytes.Buffer
	for i, s := range ss {
		if i > 0 {
			b.WriteString(sep)
		}
		b.WriteString(s)
	}
	return b.String()
}
