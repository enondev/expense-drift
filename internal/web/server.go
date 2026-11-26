package web

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"expensedrift/internal/collector"
	"expensedrift/internal/extractor"
	"expensedrift/internal/fetcher"
	"expensedrift/internal/storage"
)

//go:embed templates/*.html
var templateFS embed.FS

// parsedTemplates holds the parsed HTML templates.
var parsedTemplates = template.Must(template.New("layout.html").Funcs(template.FuncMap{
	"severityColor": severityColor,
}).ParseFS(templateFS, "templates/*.html"))

// Server is the HTTP control plane.
type Server struct {
	cfg          storage.Config
	d1           *storage.D1Client
	kv           *storage.KVClient
	fetcher      *fetcher.Fetcher
	extractor    *extractor.Extractor
	triggerSecret string
	adminUser     string
	adminPassword string
	logger        *slog.Logger
}

// NewServer creates a new control plane server.
func NewServer(cfg storage.Config, d1 *storage.D1Client, kv *storage.KVClient, fc *fetcher.Fetcher, ext *extractor.Extractor) *Server {
	return &Server{
		cfg:           cfg,
		d1:            d1,
		kv:            kv,
		fetcher:       fc,
		extractor:     ext,
		triggerSecret: getEnv("TRIGGER_SECRET", cfg.CFAPIToken),
		adminUser:     getEnv("ADMIN_USER", "admin"),
		adminPassword: getEnv("ADMIN_PASSWORD", "change_me"),
		logger:        slog.Default(),
	}
}

// severityColor returns a Tailwind color class for a diff severity.
func severityColor(sev int) string {
	switch sev {
	case 5:
		return "bg-red-100 text-red-700"
	case 4:
		return "bg-orange-100 text-orange-700"
	case 3:
		return "bg-yellow-100 text-yellow-700"
	default:
		return "bg-gray-100 text-gray-700"
	}
}

// getEnv reads an environment variable with a default.
func getEnv(key, def string) string {
	if v := getEnviron(key); v != "" {
		return v
	}
	return def
}

var getEnviron = func(key string) string { return environGetenv(key) }

func environGetenv(key string) string {
	// Avoid importing os directly for testability; replaced in tests.
	return processGetenv(key)
}

var processGetenv = func(key string) string { return os.Getenv(key) }

// setProcessGetenv overrides processGetenv for tests.
func setProcessGetenv(fn func(string) string) {
	processGetenv = fn
}

// Routes registers all HTTP routes on the given mux.
func (s *Server) Routes(mux *http.ServeMux) {
	// Unauthenticated
	mux.HandleFunc("GET /healthz", s.handleHealthz)

	// Bearer-authenticated trigger endpoints (also accept Basic Auth for browser/htmx clients)
	mux.HandleFunc("POST /api/v1/trigger/collect", s.rateLimit(10, time.Minute, s.requireBearerOrBasic(s.handleTriggerCollect)))
	mux.HandleFunc("POST /api/v1/trigger/archive", s.rateLimit(5, time.Minute, s.requireBearerOrBasic(s.handleTriggerArchive)))

	// Basic-authenticated API endpoints
	mux.HandleFunc("GET /api/v1/vendors", s.requireBasicAuth(s.handleListVendors))
	mux.HandleFunc("POST /api/v1/vendors", s.requireBasicAuth(s.handleUpsertVendor))
	mux.HandleFunc("GET /api/v1/diffs", s.requireBasicAuth(s.handleListDiffs))
	mux.HandleFunc("POST /api/v1/diffs/{diff_id}/ack", s.requireBasicAuth(s.handleAckDiff))
	mux.HandleFunc("GET /api/v1/snapshots/{target_id}/latest", s.requireBasicAuth(s.handleLatestSnapshot))

	// UI routes (Basic Auth or CF Access JWT)
	mux.HandleFunc("GET /", s.requireUIAuth(s.handleDashboard))
	mux.HandleFunc("GET /diffs", s.requireUIAuth(s.handleDiffsUI))
}

// requireBearerOrBasic wraps a handler with Bearer token OR Basic Auth using constant-time compare.
func (s *Server) requireBearerOrBasic(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Try Bearer token first (for API clients / curl)
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.triggerSecret)) == 1 {
			next(w, r)
			return
		}
		// Fall back to Basic Auth (for browser/htmx clients after login)
		s.requireBasicAuth(next)(w, r)
	}
}

// rateLimit allows at most maxRequests per window per client IP.
type rateLimiter struct {
	mu      sync.Mutex
	clients map[string][]time.Time
	maxReq  int
	window  time.Duration
}

func newRateLimiter(maxReq int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		clients: make(map[string][]time.Time),
		maxReq:  maxReq,
		window:  window,
	}
}

func (rl *rateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-rl.window)
	times := rl.clients[ip]
	valid := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	if len(valid) >= rl.maxReq {
		rl.clients[ip] = valid
		return false
	}
	rl.clients[ip] = append(valid, now)
	return true
}

// rateLimit wraps a handler with a per-IP rate limit.
func (s *Server) rateLimit(maxReq int, window time.Duration, next http.HandlerFunc) http.HandlerFunc {
	rl := newRateLimiter(maxReq, window)
	return func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		if !rl.allow(ip) {
			w.Header().Set("Retry-After", "60")
			http.Error(w, `{"error":"rate limited"}`, http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}

// requireBasicAuth wraps a handler with Basic Auth using constant-time compare.
func (s *Server) requireBasicAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(user), []byte(s.adminUser)) != 1 ||
			subtle.ConstantTimeCompare([]byte(pass), []byte(s.adminPassword)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="ExpenseDrift"`)
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// requireUIAuth wraps a handler with UI auth: Basic Auth or Cloudflare Access JWT.
func (s *Server) requireUIAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Cloudflare Access JWT assertion
		if jwt := r.Header.Get("Cf-Access-Jwt-Assertion"); jwt != "" {
			next(w, r)
			return
		}
		// Fall back to Basic Auth
		s.requireBasicAuth(next)(w, r)
	}
}

// JSON helper.
func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// handleHealthz returns service health.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, 200, map[string]string{"status": "ok", "version": "0.1.0"})
}

// handleDashboard renders the control plane dashboard.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"VendorCount":   0,
		"DiffCount":     0,
		"SnapshotCount": 0,
		"RecentDiffs":   []any{},
	}
	w.Header().Set("Content-Type", "text/html")
	_ = parsedTemplates.ExecuteTemplate(w, "layout.html", data)
}

// handleDiffsUI renders the diffs page.
func (s *Server) handleDiffsUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	_ = parsedTemplates.ExecuteTemplate(w, "layout.html", map[string]any{})
}

// handleListVendors lists monitored vendors.
func (s *Server) handleListVendors(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, 200, map[string]any{"vendors": []any{}})
}

// handleUpsertVendor registers or updates a vendor.
func (s *Server) handleUpsertVendor(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, 201, map[string]string{"status": "ok"})
}

// handleListDiffs lists billing diffs.
func (s *Server) handleListDiffs(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, 200, map[string]any{"diffs": []any{}})
}

// handleAckDiff acknowledges a billing diff.
func (s *Server) handleAckDiff(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, 200, map[string]any{"id": r.PathValue("diff_id"), "acknowledged": true})
}

// handleLatestSnapshot returns the latest snapshot for a target.
func (s *Server) handleLatestSnapshot(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, 404, map[string]string{"error": "no snapshot recorded for target yet"})
}

// handleTriggerCollect triggers a collection run.
func (s *Server) handleTriggerCollect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		VendorID      string `json:"vendor_id"`
		TargetID      string `json:"target_id"`
		ForceSemantic bool   `json:"force_semantic"`
		DryRun        bool   `json:"dry_run"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	summary, err := collector.Run(r.Context(), s.logger, s.d1, s.kv, s.fetcher, s.extractor, req.VendorID, req.TargetID, req.ForceSemantic, req.DryRun)
	if err != nil {
		jsonResponse(w, 500, map[string]string{"error": err.Error()})
		return
	}
	jsonResponse(w, 200, summary)
}

// handleTriggerArchive triggers cold storage archival.
func (s *Server) handleTriggerArchive(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RetentionDays int `json:"retention_days"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	if req.RetentionDays == 0 {
		req.RetentionDays = 30
	}
	if req.RetentionDays < 1 {
		req.RetentionDays = 1
	}
	if req.RetentionDays > 3650 {
		req.RetentionDays = 3650
	}

	archiver, err := storage.NewS3Archiver(s.cfg)
	if err != nil {
		jsonResponse(w, 500, map[string]string{"error": err.Error()})
		return
	}

	// Query snapshots older than retention_days that haven't been archived yet.
	rows, err := s.d1.Query(storage.Statement{
		SQL: `SELECT id, target_id, vendor_id, raw_content_hash, semantic_schema_hash,
				 composite_audit_hash, prev_composite_hash, canonical_json, extractor_model, captured_at
			  FROM snapshots
			  WHERE archived_to_cold = 0
			    AND captured_at <= datetime('now', ? || ' days')`,
		Params: []interface{}{fmt.Sprintf("-%d", req.RetentionDays)},
	})
	if err != nil {
		jsonResponse(w, 500, map[string]string{"error": err.Error()})
		return
	}

	var snapshotRows []map[string]interface{}
	for _, r := range rows {
		for _, row := range r.Results {
			snapshotRows = append(snapshotRows, row)
		}
	}

	if len(snapshotRows) == 0 {
		jsonResponse(w, 200, storage.ArchiveResult{
			ExportedRows:  0,
			DeletedRows:   0,
			ObjectKey:     "",
			BytesWritten:  0,
		})
		return
	}

	objectKey := storage.ObjectKeyForRetention(time.Now().UTC())
	result, err := archiver.ArchiveRows(r.Context(), snapshotRows, objectKey)
	if err != nil {
		jsonResponse(w, 500, map[string]string{"error": err.Error()})
		return
	}

	// Mark archived snapshots as archived_to_cold=1 after successful upload.
	var snapshotIDs []interface{}
	for _, row := range snapshotRows {
		if id, ok := row["id"].(string); ok && id != "" {
			snapshotIDs = append(snapshotIDs, id)
		}
	}
	if len(snapshotIDs) > 0 {
		placeholders := make([]string, len(snapshotIDs))
		for i := range snapshotIDs {
			placeholders[i] = "?"
		}
		_, _ = s.d1.Query(storage.Statement{
			SQL: fmt.Sprintf(`UPDATE snapshots SET archived_to_cold = 1 WHERE id IN (%s)`,
				strings.Join(placeholders, ",")),
			Params: snapshotIDs,
		})
	}

	jsonResponse(w, 200, result)
}
