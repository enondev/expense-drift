package storage

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// ---------- ValidateStatements ----------

func TestValidateStatementsRejectsUnindexedSelect(t *testing.T) {
	stmts := []Statement{
		{SQL: "SELECT * FROM snapshots WHERE captured_at > '2026-01-01'"}, // indexed
		{SQL: "SELECT * FROM snapshots WHERE vendor_id = 'vercel'"},       // indexed
		{SQL: "SELECT * FROM snapshots"},                                   // no WHERE -> fail
	}
	if err := ValidateStatements(stmts); err == nil {
		t.Fatal("expected error for unindexed SELECT")
	}
}

func TestValidateStatementsPassesIndexedSelects(t *testing.T) {
	stmts := []Statement{
		{SQL: "SELECT * FROM billing_diffs WHERE acknowledged = 0 AND severity > 3"},
		{SQL: "SELECT * FROM watch_targets WHERE vendor_id = 'vercel'"},
		{SQL: "SELECT * FROM vendors WHERE id = 'vercel'"},
	}
	if err := ValidateStatements(stmts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateStatementsNonSelectIgnored(t *testing.T) {
	stmts := []Statement{
		{SQL: "INSERT INTO snapshots (id, target_id) VALUES ('x', 'y')"},
		{SQL: "DELETE FROM snapshots WHERE id = 'x'"}, // DELETE has WHERE but isn't SELECT
	}
	if err := ValidateStatements(stmts); err != nil {
		t.Fatalf("non-SELECT should not be validated: %v", err)
	}
}

// ---------- D1Client ----------

type capturedD1Request struct {
	method      string
	path        string
	auth        string
	contentType string
	body        string
}

func TestD1ClientQuerySuccess(t *testing.T) {
	captured := &capturedD1Request{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.method = r.Method
		captured.path = r.URL.Path
		captured.auth = r.Header.Get("Authorization")
		captured.contentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		captured.body = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		io.WriteString(w, `{"success":true,"result":[{"success":true,"meta":{}},{"success":true,"results":[{"id":"snap-1"}]}]}`)
	}))
	defer srv.Close()

	cfg := Config{CFAccountID: "acct-123", CFAPIToken: "tok-abc", CFD1DatabaseID: "db-456"}
	dc := NewD1ClientWithURL(cfg, srv.URL)

	res, err := dc.Query(
		Statement{SQL: "SELECT * FROM snapshots WHERE target_id = 'x'", Params: []interface{}{"x"}},
	)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("expected 2 results, got %d", len(res))
	}

	if captured.method != http.MethodPost {
		t.Fatalf("expected POST, got %s", captured.method)
	}
	if captured.auth != "Bearer tok-abc" {
		t.Fatalf("expected Bearer auth, got %q", captured.auth)
	}
	if captured.contentType != "application/json" {
		t.Fatalf("expected application/json, got %q", captured.contentType)
	}
	// Verify batch statements were sent using the "statements" key (not "batch").
	if !strings.Contains(captured.body, `"statements"`) {
		t.Fatalf("expected \"statements\" key in request body, got %q", captured.body)
	}
	if strings.Contains(captured.body, `"batch"`) {
		t.Fatalf("request body must not contain \"batch\" key, got %q", captured.body)
	}
	if !strings.Contains(captured.body, "SELECT * FROM snapshots WHERE target_id = 'x'") {
		t.Fatalf("expected SQL in body, got %q", captured.body)
	}
}

func TestD1ClientQueryValidationError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not reach HTTP when validation fails")
	}))
	defer srv.Close()

	cfg := Config{CFAccountID: "acct-123", CFAPIToken: "tok-abc", CFD1DatabaseID: "db-456"}
	dc := NewD1ClientWithURL(cfg, srv.URL)

	_, err := dc.Query(Statement{SQL: "SELECT * FROM snapshots"}) // no WHERE -> validation error
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestD1ClientStatementFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"success":true,"result":[{"success":false,"meta":{"changes":-1}}]}`)
	}))
	defer srv.Close()

	cfg := Config{CFAccountID: "acct-123", CFAPIToken: "tok-abc", CFD1DatabaseID: "db-456"}
	dc := NewD1ClientWithURL(cfg, srv.URL)

	_, err := dc.Query(Statement{SQL: "UPDATE snapshots SET x = 1 WHERE id = 'y'"})
	if err == nil {
		t.Fatal("expected statement failure error")
	}
}

// ---------- KVClient ----------

func TestKVClientReadWriteManifest(t *testing.T) {
	var storedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"updated_at":"2026-01-01T00:00:00Z","targets":{"vercel:pricing":{"raw":"a","sem":"b","comp":"c"}}}`)
			return
		}
		if r.Method == http.MethodPut {
			storedBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(200)
			io.WriteString(w, `{"success":true}`)
			return
		}
		w.WriteHeader(405)
	}))
	defer srv.Close()

	cfg := Config{CFAccountID: "acct-123", CFAPIToken: "tok-abc", CFKVNamespaceID: "kv-789"}
	kc := NewKVClientWithURL(cfg, srv.URL)

	// Read
	m, err := kc.ReadManifest()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if m.Targets["vercel:pricing"].Raw != "a" || m.Targets["vercel:pricing"].Comp != "c" {
		t.Fatalf("manifest mismatch: %+v", m.Targets)
	}

	// Write
	m2 := &Manifest{
		UpdatedAt: "2026-01-02T00:00:00Z",
		Targets:   map[string]TargetHashes{"linear:pricing": {Raw: "x", Sem: "y", Comp: "z"}},
	}
	if err := kc.WriteManifest(m2); err != nil {
		t.Fatalf("write: %v", err)
	}

	var stored Manifest
	if err := json.Unmarshal(storedBody, &stored); err != nil {
		t.Fatalf("unmarshal stored body: %v", err)
	}
	if stored.Targets["linear:pricing"].Comp != "z" {
		t.Fatalf("stored manifest mismatch: %+v", stored.Targets)
	}
}

func TestKVClientReadManifest404ReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()

	cfg := Config{CFAccountID: "acct-123", CFAPIToken: "tok-abc", CFKVNamespaceID: "kv-789"}
	kc := NewKVClientWithURL(cfg, srv.URL)

	m, err := kc.ReadManifest()
	if err != nil {
		t.Fatalf("read 404: %v", err)
	}
	if len(m.Targets) != 0 {
		t.Fatal("expected empty targets map on 404")
	}
}

// ---------- countingWriter ----------

func TestCountingWriterTracksBytes(t *testing.T) {
	var buf bytes.Buffer
	count := int64(0)
	cw := &countingWriter{w: &buf, count: &count}
	n, err := cw.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != 5 {
		t.Fatalf("expected 5 bytes written, got %d", n)
	}
	if count != 5 {
		t.Fatalf("expected count 5, got %d", count)
	}
}

// ---------- S3Archiver with mock s3API ----------

type mockS3PutObject struct {
	input      *s3.PutObjectInput
	err        error
	callCount  int
	lastCtx    context.Context
	bodyBytes  []byte
}

func (m *mockS3PutObject) PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	m.callCount++
	m.lastCtx = ctx
	m.input = params
	if m.err != nil {
		return nil, m.err
	}
	// Consume the streamed body so the countingWriter in ArchiveRows sees bytes,
	// and capture it for test verification.
	if params.Body != nil {
		b, err := io.ReadAll(params.Body)
		if err == nil {
			m.bodyBytes = b
		}
	}
	return &s3.PutObjectOutput{}, nil
}

func TestS3ArchiverArchiveRowsStreamsAndCounts(t *testing.T) {
	mock := &mockS3PutObject{}
	a := NewS3ArchiverWithClient(mock, "my-bucket")

	rows := []map[string]interface{}{
		{"id": "snap-1", "vendor_id": "vercel", "canonical_json": `{"tiers":[]}`},
		{"id": "snap-2", "vendor_id": "linear", "canonical_json": `{"tiers":[]}`},
	}
	ctx := context.Background()

	res, err := a.ArchiveRows(ctx, rows, "snapshots/year=2026/month=11/day=24/2026-11-24T000000Z.jsonl.gz")
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if mock.callCount != 1 {
		t.Fatalf("expected 1 PutObject call, got %d", mock.callCount)
	}
	if mock.input == nil {
		t.Fatal("expected PutObject input")
	}
	if *mock.input.Bucket != "my-bucket" {
		t.Fatalf("expected bucket my-bucket, got %s", *mock.input.Bucket)
	}
	if *mock.input.Key != "snapshots/year=2026/month=11/day=24/2026-11-24T000000Z.jsonl.gz" {
		t.Fatalf("unexpected key: %s", *mock.input.Key)
	}
	if res.BytesWritten == 0 {
		t.Fatal("expected bytes_written > 0")
	}
	if res.ExportedRows != 2 {
		t.Fatalf("expected 2 exported rows, got %d", res.ExportedRows)
	}
	if res.DeletedRows != 2 {
		t.Fatalf("expected 2 deleted rows, got %d", res.DeletedRows)
	}

	// Verify the streamed body is valid gzip + JSONL.
	body := mock.bodyBytes
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gz.Close()
	dec := json.NewDecoder(gz)
	count := 0
	for {
		var row map[string]interface{}
		if err := dec.Decode(&row); err != nil {
			break
		}
		count++
	}
	if count != 2 {
		t.Fatalf("expected 2 JSONL records in stream, got %d", count)
	}
}

func TestS3ArchiverNoBucket(t *testing.T) {
	a := NewS3ArchiverWithClient(&mockS3PutObject{}, "")
	_, err := a.ArchiveRows(context.Background(), nil, "key.jsonl.gz")
	if err == nil {
		t.Fatal("expected error for missing bucket")
	}
	if !strings.Contains(err.Error(), "bucket not configured") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestS3ArchiverPutObjectError(t *testing.T) {
	mock := &mockS3PutObject{err: fmt.Errorf("boom")}
	a := NewS3ArchiverWithClient(mock, "my-bucket")

	rows := []map[string]interface{}{{"id": "snap-1"}}
	_, err := a.ArchiveRows(context.Background(), rows, "key.jsonl.gz")
	if err == nil {
		t.Fatal("expected PutObject error to propagate")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected boom error, got %v", err)
	}
}

func TestObjectKeyForRetention(t *testing.T) {
	key := objectKeyForRetention("2026", "11", "24")
	expected := "snapshots/year=2026/month=11/day=24/2026-11-24T000000Z.jsonl.gz"
	if key != expected {
		t.Fatalf("expected %q, got %q", expected, key)
	}
}

// Ensure imports are used.
var _ = aws.String
var _ = s3.PutObjectInput{}
