package fetcher

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// maxConcurrency bounds simultaneous outbound fetches (<= 5 per AGENTS.md).
const maxConcurrency = 5

// politenessDelay is the minimum delay between requests to the same host.
const politenessDelay = 2 * time.Second

// maxBodyBytes caps the buffered response body at 5 MB.
const maxBodyBytes = 5 << 20

// redirectLimit caps HTTP redirect hops.
const redirectLimit = 10

// blockedCIDRs lists private/loopback/link-local/multicast ranges per AGENTS.md SSRF rule.
var blockedCIDRs = []*net.IPNet{
	mustCIDR("127.0.0.0/8"),
	mustCIDR("10.0.0.0/8"),
	mustCIDR("172.16.0.0/12"),
	mustCIDR("192.168.0.0/16"),
	mustCIDR("169.254.0.0/16"),
	mustCIDR("::1/128"),
	mustCIDR("fc00::/7"),
	mustCIDR("fe80::/10"),
}

var collapseWhitespace = regexp.MustCompile(`\s+`)

func mustCIDR(cidr string) *net.IPNet {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(err)
	}
	return ipnet
}

// isBlockedIP returns true for loopback, private, link-local, and multicast IPs.
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, cidr := range blockedCIDRs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// hostLimiter enforces the per-host minimum delay between requests.
type hostLimiter struct {
	mu   sync.Mutex
	last time.Time
}

// wait blocks until the per-host politeness delay has elapsed since the last
// request to host, then records the request time.
func (h *hostLimiter) wait() {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(h.last)
	if elapsed < politenessDelay {
		time.Sleep(politenessDelay - elapsed)
	}
	h.last = time.Now()
}

// Fetcher performs SSRF-safe, bounded, polite HTTP fetches with payload
// distillation for LLM extraction.
type Fetcher struct {
	client   *http.Client
	sem      chan struct{}
	hosts    sync.Map // map[string]*hostLimiter
	// allowPrivateIPs disables SSRF blocking for tests only.
	allowPrivateIPs bool
}

// FetchResult contains the fetched and distilled page payload.
type FetchResult struct {
	URL         string
	StatusCode  int
	RawBody     []byte   // limited to maxBodyBytes; input to raw_content_hash
	Distilled   string   // stripped text for LLM consumption
	NextData    string   // __NEXT_DATA__ JSON block
	JSONLD      []string // application/ld+json blocks
	Err         error
}

// New returns a Fetcher with an SSRF-safe transport and bounded concurrency.
func New() *Fetcher {
	f := &Fetcher{
		sem: make(chan struct{}, maxConcurrency),
	}

	baseDial := &net.Dialer{Timeout: 10 * time.Second}
	dialWithSSRF := func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addrs, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		for _, ip := range addrs {
			if !f.allowPrivateIPs && isBlockedIP(ip) {
				return nil, fmt.Errorf("ssrf: blocked address %s (%s)", host, ip)
			}
		}
		return baseDial.DialContext(ctx, network, address)
	}

	transport := &http.Transport{
		DialContext:           dialWithSSRF,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          20,
		MaxIdleConnsPerHost:   5,
		MaxConnsPerHost:       5,
	}

	f.client = &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= redirectLimit {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}

	return f
}

// hostKey returns a hostLimiter for the given host, creating it if needed.
func (f *Fetcher) hostKey(host string) *hostLimiter {
	hl, _ := f.hosts.LoadOrStore(host, &hostLimiter{})
	return hl.(*hostLimiter)
}

// Fetch performs a single GET, enforcing SSRF, body limits, concurrency, and
// per-host politeness. It distills the HTML payload for LLM extraction.
func (f *Fetcher) Fetch(ctx context.Context, targetURL string) *FetchResult {
	res := &FetchResult{URL: targetURL}

	f.sem <- struct{}{}
	defer func() { <-f.sem }()

	u, err := url.Parse(targetURL)
	if err != nil {
		res.Err = fmt.Errorf("invalid url %q: %w", targetURL, err)
		return res
	}
	host := u.Hostname()

	f.hostKey(host).wait()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		res.Err = err
		return res
	}
	req.Header.Set("User-Agent", "ExpenseDrift/0.1 (+https://expensedrift.local)")

	resp, err := f.client.Do(req)
	if err != nil {
		res.Err = fmt.Errorf("fetch %q: %w", targetURL, err)
		return res
	}
	defer resp.Body.Close()

	res.StatusCode = resp.StatusCode
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		res.Err = fmt.Errorf("fetch %q: status %d", targetURL, resp.StatusCode)
		return res
	}

	limited := io.LimitReader(resp.Body, maxBodyBytes)
	body, err := io.ReadAll(limited)
	if err != nil {
		res.Err = fmt.Errorf("read body %q: %w", targetURL, err)
		return res
	}
	res.RawBody = body

	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/html") || strings.Contains(contentType, "application/xhtml") {
		res.Distilled, res.NextData, res.JSONLD = distillHTML(string(body))
	}

	return res
}

// distillHTML extracts __NEXT_DATA__ and JSON-LD blocks, strips boilerplate
// elements, and returns normalized visible text.
func distillHTML(html string) (distilled, nextData string, jsonLD []string) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return "", "", nil
	}

	// Extract __NEXT_DATA__ (Next.js hydration JSON).
	doc.Find("script#__NEXT_DATA__").Each(func(_ int, s *goquery.Selection) {
		if nextData != "" {
			return
		}
		nextData = strings.TrimSpace(s.Text())
	})

	// Extract application/ld+json JSON-LD blocks.
	doc.Find(`script[type="application/ld+json"]`).Each(func(_ int, s *goquery.Selection) {
		t := strings.TrimSpace(s.Text())
		if t != "" {
			jsonLD = append(jsonLD, t)
		}
	})

	// Strip boilerplate elements that add noise without commercial signal.
	doc.Find("script, style, svg, nav, footer, header, noscript, iframe").Remove()

	// Extract visible text, collapsing whitespace.
	text := doc.Text()
	text = collapseWhitespace.ReplaceAllString(text, " ")
	text = strings.TrimSpace(text)

	return text, nextData, jsonLD
}

// ExtractJSONLDFirstObject returns the first JSON-LD block parsed into a map,
// or nil if none is valid JSON.
func ExtractJSONLDFirstObject(jsonLD []string) map[string]interface{} {
	for _, block := range jsonLD {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(block), &m); err == nil {
			return m
		}
		// Some pages wrap JSON-LD in an array.
		var arr []map[string]interface{}
		if err := json.Unmarshal([]byte(block), &arr); err == nil && len(arr) > 0 {
			return arr[0]
		}
	}
	return nil
}
