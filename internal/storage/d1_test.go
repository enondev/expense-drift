package storage

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type capturedD1Request2 struct {
	method string
	path   string
	auth   string
	body   string
}

func TestD1ClientWireFormatUsesStatementsKey(t *testing.T) {
	captured := &capturedD1Request2{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.method = r.Method
		captured.path = r.URL.Path
		captured.auth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		captured.body = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		io.WriteString(w, `{"success":true,"result":[{"success":true,"meta":{}}]}`)
	}))
	defer srv.Close()

	cfg := Config{CFAccountID: "acct-123", CFAPIToken: "tok-abc", CFD1DatabaseID: "db-456"}
	dc := NewD1ClientWithURL(cfg, srv.URL)

	_, err := dc.Query(
		Statement{SQL: "INSERT INTO snapshots (id, target_id) VALUES (?, ?)", Params: []interface{}{"snap-1", "tgt-1"}},
		Statement{SQL: "UPDATE snapshots SET archived_to_cold = 1 WHERE id = ?", Params: []interface{}{"snap-1"}},
	)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if captured.method != http.MethodPost {
		t.Fatalf("expected POST, got %s", captured.method)
	}
	if captured.auth != "Bearer tok-abc" {
		t.Fatalf("expected Bearer auth, got %q", captured.auth)
	}

	// The wire format MUST use "statements", not "batch".
	if !strings.Contains(captured.body, `"statements"`) {
		t.Fatalf("expected \"statements\" key in request body, got %q", captured.body)
	}
	if strings.Contains(captured.body, `"batch"`) {
		t.Fatalf("request body must not contain \"batch\" key, got %q", captured.body)
	}

	var dqr d1QueryRequest
	if err := json.Unmarshal([]byte(captured.body), &dqr); err != nil {
		t.Fatalf("unmarshal d1QueryRequest: %v", err)
	}
	if len(dqr.Statements) != 2 {
		t.Fatalf("expected 2 statements, got %d", len(dqr.Statements))
	}
	if dqr.Statements[0].SQL != "INSERT INTO snapshots (id, target_id) VALUES (?, ?)" {
		t.Fatalf("unexpected first statement SQL: %q", dqr.Statements[0].SQL)
	}
}

func TestD1ClientRejectsStatementFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"success":true,"result":[{"success":false,"meta":{"changes":-1}}]}`)
	}))
	defer srv.Close()

	cfg := Config{CFAccountID: "acct-123", CFAPIToken: "tok-abc", CFD1DatabaseID: "db-456"}
	dc := NewD1ClientWithURL(cfg, srv.URL)

	_, err := dc.Query(Statement{SQL: "DELETE FROM snapshots WHERE id = ?", Params: []interface{}{"x"}})
	if err == nil {
		t.Fatal("expected statement failure error")
	}
	if !strings.Contains(err.Error(), "d1 statement failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestD1ClientRejectsNon2xxStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		io.WriteString(w, `{"success":false,"errors":["internal error"]}`)
	}))
	defer srv.Close()

	cfg := Config{CFAccountID: "acct-123", CFAPIToken: "tok-abc", CFD1DatabaseID: "db-456"}
	dc := NewD1ClientWithURL(cfg, srv.URL)

	_, err := dc.Query(Statement{SQL: "SELECT 1 WHERE id = 'x'"})
	if err == nil {
		t.Fatal("expected non-2xx status error")
	}
	if !strings.Contains(err.Error(), "d1 query status 500") {
		t.Fatalf("unexpected error: %v", err)
	}
}
