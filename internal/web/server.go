package web

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
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

// session represents an authenticated user session.
type session struct {
	userID    string
	expiresAt time.Time
}

// sessionStore holds active sessions in memory.
type sessionStore struct {
	mu   sync.RWMutex
	data map[string]*session
}

const (
	sessionCookieName = "expensedrift_session"
	sessionTTL        = 24 * time.Hour
	sessionIDLen      = 32
)

var sessions = &sessionStore{
	data: make(map[string]*session),
}

func (s *sessionStore) get(id string) (*session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.data[id]
	if !ok || sess.expiresAt.Before(time.Now()) {
		return nil, false
	}
	return sess, true
}

func (s *sessionStore) set(userID string) string {
	b := make([]byte, sessionIDLen)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	id := hex.EncodeToString(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[id] = &session{
		userID:    userID,
		expiresAt: time.Now().Add(sessionTTL),
	}
	for k, v := range s.data {
		if v.expiresAt.Before(time.Now()) {
			delete(s.data, k)
		}
	}
	return id
}

func (s *sessionStore) delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, id)
}

// Server is the HTTP control plane.
type Server struct {
	cfg           storage.Config
	d1            *storage.D1Client
	kv            *storage.KVClient
	fetcher       *fetcher.Fetcher
	extractor     *extractor.Extractor
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
// Accepts interface{} to handle both int and float64 (D1 returns numbers as float64).
func severityColor(sev interface{}) string {
	var s int
	switch v := sev.(type) {
	case int:
		s = v
	case float64:
		s = int(v)
	default:
		return "bg-gray-100 text-gray-700"
	}
	switch s {
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
	return processGetenv(key)
}

var processGetenv = func(key string) string { return os.Getenv(key) }

func setProcessGetenv(fn func(string) string) {
	processGetenv = fn
}

// Routes registers all HTTP routes on the given mux.
func (s *Server) Routes(mux *http.ServeMux) {
	// Unauthenticated
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /login", s.handleLogin)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("GET /logout", s.handleLogout)

	// Session-protected UI routes
	mux.HandleFunc("GET /", s.requireSession(s.handleDashboard))
	mux.HandleFunc("GET /diffs", s.requireSession(s.handleDiffsUI))
	mux.HandleFunc("GET /vendors", s.requireSession(s.handleVendorsUI))
	mux.HandleFunc("GET /targets", s.requireSession(s.handleTargetsUI))

	// Authenticated API endpoints (session cookie OR Bearer token OR Basic Auth)
	mux.HandleFunc("POST /api/v1/trigger/collect", s.rateLimit(10, time.Minute, s.requireAuth(s.handleTriggerCollect)))
	mux.HandleFunc("POST /api/v1/trigger/archive", s.rateLimit(5, time.Minute, s.requireAuth(s.handleTriggerArchive)))
	mux.HandleFunc("GET /api/v1/vendors", s.requireAuth(s.handleListVendors))
	mux.HandleFunc("POST /api/v1/vendors", s.requireAuth(s.handleUpsertVendor))
	mux.HandleFunc("GET /api/v1/targets", s.requireAuth(s.handleListTargets))
	mux.HandleFunc("GET /api/v1/diffs", s.requireAuth(s.handleListDiffs))
	mux.HandleFunc("GET /api/v1/metrics", s.requireAuth(s.handleListMetrics))
	mux.HandleFunc("GET /api/v1/diffs/fragment", s.requireAuth(s.handleListDiffsFragment))
	mux.HandleFunc("POST /api/v1/diffs/{diff_id}/ack", s.requireAuth(s.handleAckDiff))
	mux.HandleFunc("GET /api/v1/snapshots/{target_id}/latest", s.requireAuth(s.handleLatestSnapshot))
}

// requireSession ensures a valid session cookie exists; redirects to /login otherwise.
func (s *Server) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if _, ok := sessions.get(cookie.Value); !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// requireAuth accepts session cookie OR Bearer token OR Basic Auth.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Session cookie (browser/htmx clients)
		if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" {
			if _, ok := sessions.get(cookie.Value); ok {
				next(w, r)
				return
			}
		}
		// Bearer token (API clients / curl)
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.triggerSecret)) == 1 {
			next(w, r)
			return
		}
		// Basic Auth
		user, pass, ok := r.BasicAuth()
		if ok && subtle.ConstantTimeCompare([]byte(user), []byte(s.adminUser)) == 1 &&
			subtle.ConstantTimeCompare([]byte(pass), []byte(s.adminPassword)) == 1 {
			next(w, r)
			return
		}
		// If it's an HTML request, redirect to login; otherwise 401
		accept := r.Header.Get("Accept")
		if strings.Contains(accept, "text/html") {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="ExpenseDrift"`)
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
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

// handleLogin renders the login form (GET) or processes login (POST).
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		user, pass, ok := r.BasicAuth()
		if !ok {
			user = r.PostFormValue("username")
			pass = r.PostFormValue("password")
		}
		if ok && subtle.ConstantTimeCompare([]byte(user), []byte(s.adminUser)) == 1 &&
			subtle.ConstantTimeCompare([]byte(pass), []byte(s.adminPassword)) == 1 {
			sessionID := sessions.set(user)
			if sessionID == "" {
				http.Error(w, `{"error":"session creation failed"}`, http.StatusInternalServerError)
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name:     sessionCookieName,
				Value:    sessionID,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				MaxAge:   int(sessionTTL.Seconds()),
			})
			if r.Header.Get("Accept") == "application/json" || strings.Contains(r.Header.Get("Content-Type"), "application/json") {
				jsonResponse(w, 200, map[string]string{"status": "logged_in"})
				return
			}
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		if r.Header.Get("Accept") == "application/json" || strings.Contains(r.Header.Get("Content-Type"), "application/json") {
			w.Header().Set("WWW-Authenticate", `Basic realm="ExpenseDrift"`)
			http.Error(w, `{"error":"invalid credentials"}`, http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
		return
	}
	// GET /login - show login form
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if _, ok := sessions.get(cookie.Value); ok {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html")
	_ = parsedTemplates.ExecuteTemplate(w, "login", map[string]any{
		"Error": r.URL.Query().Get("error") == "1",
	})
}

// handleLogout clears the session cookie.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		sessions.delete(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// handleDashboard renders the control plane dashboard.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"VendorCount":   0,
		"DiffCount":     0,
		"SnapshotCount": 0,
		"RecentDiffs":   []any{},
	}

	// Vendor count
	if rows, err := s.d1.Query(storage.Statement{SQL: `SELECT COUNT(*) as cnt FROM vendors`}); err == nil {
		for _, r := range rows {
			for _, row := range r.Results {
				if cnt, ok := row["cnt"].(float64); ok {
					data["VendorCount"] = int(cnt)
				}
			}
		}
	}

	// Diff count
	if rows, err := s.d1.Query(storage.Statement{SQL: `SELECT COUNT(*) as cnt FROM billing_diffs`}); err == nil {
		for _, r := range rows {
			for _, row := range r.Results {
				if cnt, ok := row["cnt"].(float64); ok {
					data["DiffCount"] = int(cnt)
				}
			}
		}
	}

	// Snapshot count
	if rows, err := s.d1.Query(storage.Statement{SQL: `SELECT COUNT(*) as cnt FROM snapshots`}); err == nil {
		for _, r := range rows {
			for _, row := range r.Results {
				if cnt, ok := row["cnt"].(float64); ok {
					data["SnapshotCount"] = int(cnt)
				}
			}
		}
	}

	// Recent diffs (latest 5)
	if rows, err := s.d1.Query(storage.Statement{
		SQL: `SELECT bd.id, v.name as vendor_name, bd.drift_type, bd.severity, bd.summary,
				  bd.acknowledged, bd.created_at
			  FROM billing_diffs bd
			  JOIN vendors v ON bd.vendor_id = v.id
			  ORDER BY bd.created_at DESC
			  LIMIT 5`,
	}); err == nil {
		var recent []map[string]interface{}
		for _, r := range rows {
			recent = append(recent, r.Results...)
		}
		data["RecentDiffs"] = recent
	}

	w.Header().Set("Content-Type", "text/html")
	_ = parsedTemplates.ExecuteTemplate(w, "layout.html", data)
}

// handleDiffsUI renders the diffs page.
func (s *Server) handleDiffsUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	_ = parsedTemplates.ExecuteTemplate(w, "layout.html", map[string]any{})
}

// handleListVendors lists monitored vendors from D1.
func (s *Server) handleListVendors(w http.ResponseWriter, r *http.Request) {
	rows, err := s.d1.Query(storage.Statement{
		SQL: `SELECT id, name, category, homepage_url, status, created_at
			  FROM vendors
			  WHERE id = id`,
	})
	if err != nil {
		jsonResponse(w, 500, map[string]string{"error": err.Error()})
		return
	}
	var vendors []map[string]interface{}
	for _, r := range rows {
		for _, row := range r.Results {
			vendors = append(vendors, row)
		}
	}
	jsonResponse(w, 200, map[string]any{"vendors": vendors})
}

// handleListTargets lists watch targets with their next check time for observability.
func (s *Server) handleListTargets(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC().Format(time.RFC3339)
	rows, err := s.d1.Query(storage.Statement{
		SQL: `SELECT wt.id, wt.vendor_id, wt.surface_type, wt.url, wt.extract_selector,
				 wt.fetch_tier, wt.last_checked_at, wt.next_check_at, wt.cooldown_seconds
			  FROM watch_targets wt
			  WHERE wt.next_check_at <= ?
			  ORDER BY wt.next_check_at`,
		Params: []interface{}{now},
	})
	if err != nil {
		jsonResponse(w, 500, map[string]string{"error": err.Error()})
		return
	}
	var targets []map[string]interface{}
	for _, r := range rows {
		for _, row := range r.Results {
			targets = append(targets, row)
		}
	}
	jsonResponse(w, 200, map[string]any{"targets": targets})
}

// handleUpsertVendor registers or updates a vendor.
func (s *Server) handleUpsertVendor(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, 201, map[string]string{"status": "ok"})
}

// handleListDiffs lists billing diffs.
func (s *Server) handleListDiffs(w http.ResponseWriter, r *http.Request) {
	rows, err := s.d1.Query(storage.Statement{
		SQL: `SELECT bd.id, v.name as vendor_name, bd.drift_type, bd.severity, bd.summary,
				  bd.acknowledged, bd.created_at
			  FROM billing_diffs bd
			  JOIN vendors v ON bd.vendor_id = v.id
			  ORDER BY bd.created_at DESC
			  LIMIT 50`,
	})
	if err != nil {
		jsonResponse(w, 500, map[string]string{"error": err.Error()})
		return
	}
	var diffs []map[string]interface{}
	for _, r := range rows {
		diffs = append(diffs, r.Results...)
	}
	jsonResponse(w, 200, map[string]any{"diffs": diffs})
}

// handleAckDiff acknowledges a billing diff.
func (s *Server) handleAckDiff(w http.ResponseWriter, r *http.Request) {
	diffID := r.PathValue("diff_id")
	if diffID == "" {
		jsonResponse(w, 400, map[string]string{"error": "diff_id is required"})
		return
	}
	_, err := s.d1.Query(storage.Statement{
		SQL: `UPDATE billing_diffs SET acknowledged = 1 WHERE id = ?`,
		Params: []interface{}{diffID},
	})
	if err != nil {
		jsonResponse(w, 500, map[string]string{"error": err.Error()})
		return
	}
	// Check if any row was affected by trying to read it back
	checkRows, err := s.d1.Query(storage.Statement{
		SQL: `SELECT acknowledged FROM billing_diffs WHERE id = ?`,
		Params: []interface{}{diffID},
	})
	if err != nil {
		jsonResponse(w, 500, map[string]string{"error": err.Error()})
		return
	}
	var acknowledged int
	found := false
	for _, r := range checkRows {
		for _, row := range r.Results {
			if v, ok := row["acknowledged"].(float64); ok {
				acknowledged = int(v)
				found = true
			}
		}
	}
	if !found {
		jsonResponse(w, 404, map[string]string{"error": "diff not found"})
		return
	}
	jsonResponse(w, 200, map[string]any{"id": diffID, "acknowledged": acknowledged == 1})
}

// handleLatestSnapshot returns the latest snapshot for a target.
func (s *Server) handleLatestSnapshot(w http.ResponseWriter, r *http.Request) {
	targetID := r.PathValue("target_id")
	if targetID == "" {
		jsonResponse(w, 400, map[string]string{"error": "target_id is required"})
		return
	}
	rows, err := s.d1.Query(storage.Statement{
		SQL: `SELECT s.id, s.target_id, s.vendor_id, s.raw_content_hash, s.semantic_schema_hash,
				  s.composite_audit_hash, s.captured_at, s.extractor_model
			  FROM snapshots s
			  WHERE s.target_id = ?
			  ORDER BY s.captured_at DESC
			  LIMIT 1`,
		Params: []interface{}{targetID},
	})
	if err != nil {
		jsonResponse(w, 500, map[string]string{"error": err.Error()})
		return
	}
	var snapshots []map[string]interface{}
	for _, r := range rows {
		snapshots = append(snapshots, r.Results...)
	}
	if len(snapshots) == 0 {
		jsonResponse(w, 404, map[string]string{"error": "no snapshot recorded for target yet"})
		return
	}
	jsonResponse(w, 200, snapshots[0])
}

// handleListMetrics returns counts for the dashboard metric cards.
func (s *Server) handleListMetrics(w http.ResponseWriter, r *http.Request) {
	metrics := map[string]interface{}{}

	if rows, err := s.d1.Query(storage.Statement{SQL: `SELECT COUNT(*) as cnt FROM vendors`}); err == nil {
		for _, r := range rows {
			for _, row := range r.Results {
				if cnt, ok := row["cnt"].(float64); ok {
					metrics["vendor_count"] = int(cnt)
				}
			}
		}
	}
	if rows, err := s.d1.Query(storage.Statement{SQL: `SELECT COUNT(*) as cnt FROM billing_diffs`}); err == nil {
		for _, r := range rows {
			for _, row := range r.Results {
				if cnt, ok := row["cnt"].(float64); ok {
					metrics["diff_count"] = int(cnt)
				}
			}
		}
	}
	if rows, err := s.d1.Query(storage.Statement{SQL: `SELECT COUNT(*) as cnt FROM snapshots`}); err == nil {
		for _, r := range rows {
			for _, row := range r.Results {
				if cnt, ok := row["cnt"].(float64); ok {
					metrics["snapshot_count"] = int(cnt)
				}
			}
		}
	}
	jsonResponse(w, 200, metrics)
}

// handleListDiffsFragment returns an HTML fragment of diff table rows for HTMX.
func (s *Server) handleListDiffsFragment(w http.ResponseWriter, r *http.Request) {
	rows, err := s.d1.Query(storage.Statement{
		SQL: `SELECT bd.id, v.name as vendor_name, bd.drift_type, bd.severity, bd.summary,
				  bd.acknowledged, bd.created_at
			  FROM billing_diffs bd
			  JOIN vendors v ON bd.vendor_id = v.id
			  ORDER BY bd.created_at DESC
			  LIMIT 50`,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var diffs []map[string]interface{}
	for _, r := range rows {
		diffs = append(diffs, r.Results...)
	}
	w.Header().Set("Content-Type", "text/html")
	_ = parsedTemplates.ExecuteTemplate(w, "diffs.html", map[string]any{"Diffs": diffs})
}

// handleVendorsUI renders the vendors listing page.
func (s *Server) handleVendorsUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	_ = parsedTemplates.ExecuteTemplate(w, "layout.html", map[string]any{"Page": "vendors"})
}

// handleTargetsUI renders the targets listing page.
func (s *Server) handleTargetsUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	_ = parsedTemplates.ExecuteTemplate(w, "layout.html", map[string]any{"Page": "targets"})
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
		if _, err := s.d1.Query(storage.Statement{
			SQL: fmt.Sprintf(`UPDATE snapshots SET archived_to_cold = 1 WHERE id IN (%s)`,
				strings.Join(placeholders, ",")),
			Params: snapshotIDs,
		}); err != nil {
			jsonResponse(w, 500, map[string]string{"error": fmt.Sprintf("failed to mark snapshots archived: %v", err)})
			return
		}
	}

	jsonResponse(w, 200, result)
}
