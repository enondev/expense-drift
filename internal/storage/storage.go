package storage

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// Statement is a single D1 SQL statement for batched query execution.
type Statement struct {
	SQL    string         `json:"sql"`
	Params []interface{} `json:"params,omitempty"`
}

// Result is the result of a single D1 statement execution.
type Result struct {
	Success bool                   `json:"success"`
	Meta    map[string]interface{} `json:"meta,omitempty"`
	Results []map[string]interface{} `json:"results,omitempty"`
}

// QueryResponse is the top-level D1 API response.
type QueryResponse struct {
	Success  bool     `json:"success"`
	Errors   []string `json:"errors,omitempty"`
	Messages []string `json:"messages,omitempty"`
	Result   []Result `json:"result,omitempty"`
}

// IndexedColumn is a column that has a covering index in the D1 schema.
// Every SELECT must filter on at least one of these to protect the 5M daily
// row-read cap (ADR-002).
var indexedColumns = map[string]bool{
	"vendors.id":                  true,
	"watch_targets.id":            true,
	"watch_targets.vendor_id":     true,
	"snapshots.id":                true,
	"snapshots.vendor_id":         true,
	"snapshots.target_id":         true,
	"snapshots.captured_at":       true,
	"snapshots.archived_to_cold":  true,
	"billing_diffs.id":            true,
	"billing_diffs.vendor_id":     true,
	"billing_diffs.target_id":     true,
	"billing_diffs.acknowledged":  true,
	"billing_diffs.severity":      true,
	"billing_diffs.created_at":    true,
}

var selectRegexp = regexp.MustCompile(`(?is)^\s*select\b`)
var whereRegexp = regexp.MustCompile(`(?is)\bwhere\b`)
var columnRefRegexp = regexp.MustCompile(`(?i)([a-z_][a-z0-9_]*)\.([a-z_][a-z0-9_]*)`)
var bareColumnRegexp = regexp.MustCompile(`(?i)\b([a-z_][a-z0-9_]*)\s*(?:=|!=|<>|>|<|>=|<=|IN|in|LIKE|like)`)

// indexedColumnNames is the set of indexed column names (without table prefix)
// so unqualified references like "acknowledged = 0" also pass the check.
var indexedColumnNames = map[string]bool{
	"id":                 true,
	"vendor_id":          true,
	"target_id":          true,
	"captured_at":        true,
	"archived_to_cold":   true,
	"acknowledged":       true,
	"severity":           true,
	"created_at":         true,
}

// validateSelectFilters checks that a SELECT statement filters on an indexed
// column via a WHERE clause. Returns an error if the check fails.
func validateSelectFilters(sql string) error {
	if !selectRegexp.MatchString(sql) {
		return nil
	}
	if !whereRegexp.MatchString(sql) {
		return fmt.Errorf("d1: SELECT must filter on an indexed column via WHERE: %.100s", sql)
	}

	// Check table.column references first.
	for _, m := range columnRefRegexp.FindAllStringSubmatch(sql, -1) {
		if len(m) >= 3 && (indexedColumns[m[2]] || indexedColumnNames[m[2]]) {
			return nil
		}
	}
	// Then check bare column identifiers.
	for _, m := range bareColumnRegexp.FindAllStringSubmatch(sql, -1) {
		if len(m) >= 2 && indexedColumnNames[m[1]] {
			return nil
		}
	}
	return fmt.Errorf("d1: SELECT must filter on an indexed column (vendors.id, watch_targets.vendor_id, snapshots.target_id, billing_diffs.acknowledged, etc.): %.100s", sql)
}

// ValidateStatements checks every SELECT in a batch filters on indexed columns.
func ValidateStatements(stmts []Statement) error {
	for _, s := range stmts {
		if err := validateSelectFilters(s.SQL); err != nil {
			return err
		}
	}
	return nil
}

// Config holds all storage configuration from environment variables.
type Config struct {
	// Cloudflare D1
	CFAccountID     string
	CFAPIToken      string
	CFD1DatabaseID  string

	// Cloudflare KV
	CFKVNamespaceID string

	// Cold Storage (S3-compatible: Backblaze B2 or Tigris)
	S3Endpoint        string
	S3Bucket          string
	S3AccessKeyID     string
	S3SecretAccessKey string
}

// FromEnv loads configuration from environment variables.
func FromEnv() Config {
	return Config{
		CFAccountID:     getEnv("CF_ACCOUNT_ID", ""),
		CFAPIToken:      getEnv("CF_API_TOKEN", ""),
		CFD1DatabaseID:  getEnv("CF_D1_DATABASE_ID", ""),
		CFKVNamespaceID: getEnv("CF_KV_NAMESPACE_ID", ""),
		S3Endpoint:      getEnv("S3_ENDPOINT", ""),
		S3Bucket:        getEnv("S3_BUCKET", ""),
		S3AccessKeyID:   getEnv("S3_ACCESS_KEY_ID", ""),
		S3SecretAccessKey: getEnv("S3_SECRET_ACCESS_KEY", ""),
	}
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// countingWriter wraps an io.Writer and tracks bytes written atomically.
type countingWriter struct {
	w     io.Writer
	count *int64
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	if n > 0 {
		atomic.AddInt64(cw.count, int64(n))
	}
	return n, err
}

// Ensure imports are used.
var _ = http.StatusOK
var _ = strings.TrimSpace

// ObjectKeyForRetention builds a dated S3 object key for cold archives.
func ObjectKeyForRetention(t time.Time) string {
	return fmt.Sprintf("snapshots/year=%s/month=%s/day=%s/%s.jsonl.gz",
		t.Format("2006"), t.Format("01"), t.Format("02"), t.Format("2006-01-02T150405Z"))
}
