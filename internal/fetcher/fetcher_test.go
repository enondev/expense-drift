package fetcher

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetcherRejectsLoopbackIP(t *testing.T) {
	f := New()
	ctx := context.Background()
	res := f.Fetch(ctx, "http://127.0.0.1:12345/")
	if res.Err == nil {
		t.Fatal("expected SSRF block for 127.0.0.1")
	}
	if !strings.Contains(res.Err.Error(), "ssrf") {
		t.Fatalf("expected ssrf error, got %v", res.Err)
	}
}

func TestFetcherFetchesHTMLAndDistills(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(200)
		io.WriteString(w, `<html>
<head><script id="__NEXT_DATA__" type="application/json">{"buildId":"abc"}</script></head>
<body>
<nav>Navigation</nav>
<footer>Footer</footer>
<script>console.log("x")</script>
<style>body{}</style>
<main>Pro plan $29/month with SSO and audit logs</main>
</body>
</html>`)
	}))
	defer srv.Close()

	f := New()
	f.allowPrivateIPs = true // httptest binds to 127.0.0.1
	ctx := context.Background()
	res := f.Fetch(ctx, srv.URL)
	if res.Err != nil {
		t.Fatalf("fetch: %v", res.Err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}
	if res.NextData == "" {
		t.Fatal("expected __NEXT_DATA__ extraction")
	}
	if !strings.Contains(res.NextData, "abc") {
		t.Fatalf("expected buildId in nextData, got %q", res.NextData)
	}
	if strings.Contains(strings.ToLower(res.Distilled), "navigation") {
		t.Fatal("expected nav to be stripped from distilled text")
	}
	if !strings.Contains(res.Distilled, "Pro plan") {
		t.Fatal("expected main content preserved in distilled text")
	}
	if len(res.RawBody) == 0 {
		t.Fatal("expected raw body for hash")
	}
}

func TestFetcherHonorsPolitenessDelay(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		io.WriteString(w, "ok")
	}))
	defer srv.Close()

	f := New()
	f.allowPrivateIPs = true
	ctx := context.Background()
	start := time.Now()
	res1 := f.Fetch(ctx, srv.URL)
	res2 := f.Fetch(ctx, srv.URL)
	elapsed := time.Since(start)
	if res1.Err != nil || res2.Err != nil {
		t.Fatalf("fetch errors: %v %v", res1.Err, res2.Err)
	}
	if elapsed < politenessDelay {
		t.Fatalf("expected politeness delay >= %v, got %v", politenessDelay, elapsed)
	}
}

func TestIsBlockedIP(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"127.0.0.1", true},
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"192.168.1.1", true},
		{"169.254.1.1", true},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"::1", true},
		{"fe80::1", true},
		{"fc00::1", true},
		{"2001:db8::1", false},
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if got := isBlockedIP(ip); got != c.want {
			t.Errorf("isBlockedIP(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}
