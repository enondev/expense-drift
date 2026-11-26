package storage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// TargetHashes stores the three cascade hashes for a single watch target.
type TargetHashes struct {
	Raw   string `json:"raw"`
	Sem   string `json:"sem"`
	Comp  string `json:"comp"`
}

// Manifest is the single-key KV manifest (key: manifest:v1) that caches the
// latest hashes per target so unchanged pages exit after 1 KV read (0 D1
// reads/writes), keeping daily D1 usage under ~5% of free quotas (ADR-002).
type Manifest struct {
	UpdatedAt string                 `json:"updated_at"`
	Targets   map[string]TargetHashes `json:"targets"`
}

// KVClient wraps the Cloudflare KV REST API for the single manifest:v1 key.
type KVClient struct {
	httpClient *http.Client
	config     Config
	baseURL    string // overrides kvURL for tests
}

// NewKVClient creates a KV client from configuration.
func NewKVClient(cfg Config) *KVClient {
	return &KVClient{
		httpClient: &http.Client{Timeout: 15 * time.Second},
		config:     cfg,
	}
}

// NewKVClientWithURL creates a KV client pointed at a custom base URL (tests).
func NewKVClientWithURL(cfg Config, baseURL string) *KVClient {
	return &KVClient{
		httpClient: &http.Client{Timeout: 15 * time.Second},
		config:     cfg,
		baseURL:    baseURL,
	}
}

// kvURL builds the Cloudflare KV REST API URL for the manifest:v1 key.
func kvURL(cfg Config) string {
	return fmt.Sprintf(
		"https://api.cloudflare.com/client/v4/accounts/%s/storage/kv/namespaces/%s/values/manifest:v1",
		cfg.CFAccountID, cfg.CFKVNamespaceID,
	)
}

// ReadManifest performs the single KV read at collector startup.
func (c *KVClient) ReadManifest() (*Manifest, error) {
	url := c.baseURL
	if url == "" {
		url = kvURL(c.config)
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.config.CFAPIToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kv read request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return &Manifest{Targets: make(map[string]TargetHashes)}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("kv read status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read kv response: %w", err)
	}

	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("decode kv manifest: %w", err)
	}
	if m.Targets == nil {
		m.Targets = make(map[string]TargetHashes)
	}
	return &m, nil
}

// WriteManifest performs the single KV write at collector completion.
func (c *KVClient) WriteManifest(m *Manifest) error {
	url := c.baseURL
	if url == "" {
		url = kvURL(c.config)
	}

	body, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal kv manifest: %w", err)
	}

	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.config.CFAPIToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("kv write request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("kv write status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}
