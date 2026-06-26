package deliveroo

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	fhttp "github.com/bogdanfinn/fhttp"
)

// These tests are a gated, offline (echo-service only) verification harness for
// the iOS fingerprint hardening. They are skipped unless DELIVEROO_FP_TEST=1 —
// never run in CI and they NEVER touch Deliveroo (only tls.peet.ws + httpbin.org).
//
// Capture before/after fingerprints by selecting the profile, e.g.:
//
//	DELIVEROO_FP_TEST=1 DELIVEROO_TLS_PROFILE=ios18 go test ./internal/deliveroo -run Fingerprint -v
//	DELIVEROO_FP_TEST=1 DELIVEROO_TLS_PROFILE=ios26 go test ./internal/deliveroo -run Fingerprint -v
func skipUnlessFP(t *testing.T) {
	t.Helper()
	if os.Getenv("DELIVEROO_FP_TEST") == "" {
		t.Skip("set DELIVEROO_FP_TEST=1 to run the live fingerprint echo checks")
	}
}

// fpClient builds a real Client with a synthetic-but-complete iOS header block so
// doGET exercises the exact header order, pseudo-header order, Accept-Encoding,
// and cookie jar the live path uses. The seeded Cookie deliberately includes a
// stale __cf_bm to prove it is NOT replayed (see TestCookieRoundTrip).
func fpClient() *Client {
	c := NewClient()
	c.SetAuth("Basic Zm9vOmJhcg==", map[string]string{
		"X-Roo-App-Version":       "3.328.0",
		"X-Roo-Country":           "uk",
		"X-Roo-Platform":          "iOS",
		"Accept-Language":         "en-US,en;q=0.9",
		"Accept-Encoding":         "gzip, deflate, br",
		"X-Roo-Rooblocks-Version": "5.3.0",
		"X-Roo-Sticky-Guid":       "60FA5E87-5DDF-40DD-A9E7-83C4E29B8D45",
		"Accept":                  "*/*",
		"User-Agent":              "Deliveroo-OrderApp/3.328.0 (iPhone18,4; iOS27.0; Release; en_US; 530840)",
		"X-Roo-Guid":              "7D9C7E1D-207B-47AC-9A34-B0C331B1F530",
		"Cookie":                  "roo_super_properties=stub; __cf_bm=STALE_SHOULD_NOT_BE_SENT; roo_session_guid=stub; roo_guid=stub",
	})
	return c
}

// peetResponse maps only the fields we read from tls.peet.ws/api/all. The full
// payload is large; unmapped keys are ignored.
type peetResponse struct {
	HTTPVersion string `json:"http_version"`
	UserAgent   string `json:"user_agent"`
	TLS         struct {
		JA3           string `json:"ja3"`
		JA3Hash       string `json:"ja3_hash"`
		JA4           string `json:"ja4"`
		Peetprint     string `json:"peetprint"`
		PeetprintHash string `json:"peetprint_hash"`
	} `json:"tls"`
	HTTP2 struct {
		AkamaiFingerprint     string `json:"akamai_fingerprint"`
		AkamaiFingerprintHash string `json:"akamai_fingerprint_hash"`
	} `json:"http2"`
}

// TestIOSFingerprint prints the JA3/JA4/peetprint/akamai fingerprints observed
// through the full client, plus the negotiated HTTP version + ALPN. Eyeball the
// output: it should read as an iPhone, on h2, not Go's default.
func TestIOSFingerprint(t *testing.T) {
	skipUnlessFP(t)
	c := fpClient()

	const endpoint = "https://tls.peet.ws/api/all"
	status, body, err := c.doGET(endpoint, c.token, c.headers)
	if err != nil {
		t.Skipf("%s unreachable: %v", endpoint, err)
	}
	if status != 200 {
		t.Skipf("%s returned HTTP %d (echo unavailable): %s", endpoint, status, snippet(body, 200))
	}
	var pr peetResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		t.Fatalf("%s: decode failed (compression not handled?): %v (%s)", endpoint, err, snippet(body, 300))
	}
	t.Logf("%s -> HTTP %d", endpoint, status)
	t.Logf("  http_version : %s", pr.HTTPVersion)
	t.Logf("  ja3          : %s", pr.TLS.JA3)
	t.Logf("  ja3_hash     : %s", pr.TLS.JA3Hash)
	t.Logf("  ja4          : %s", pr.TLS.JA4)
	t.Logf("  peetprint    : %s", pr.TLS.Peetprint)
	t.Logf("  peetprint_h  : %s", pr.TLS.PeetprintHash)
	t.Logf("  akamai_fp    : %s", pr.HTTP2.AkamaiFingerprint)
	t.Logf("  akamai_fp_h  : %s", pr.HTTP2.AkamaiFingerprintHash)
	t.Logf("  echoed UA    : %s", pr.UserAgent)

	// Confirm the negotiated protocol + ALPN directly off the connection.
	tc, err := newIOSClient()
	if err != nil {
		t.Fatalf("newIOSClient: %v", err)
	}
	req, _ := fhttp.NewRequest("GET", "https://tls.peet.ws/api/clean", nil)
	req.Header[fhttp.HeaderOrderKey] = iosHeaderOrder
	req.Header[fhttp.PHeaderOrderKey] = iosPHeaderOrder
	resp, err := tc.Do(req)
	if err != nil {
		t.Skipf("proto check unreachable: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	alpn := ""
	if resp.TLS != nil {
		alpn = resp.TLS.NegotiatedProtocol
	}
	t.Logf("negotiated: proto=%q alpn=%q", resp.Proto, alpn)
	if resp.Proto != "HTTP/2.0" || alpn != "h2" {
		t.Errorf("expected HTTP/2 + ALPN h2, got proto=%q alpn=%q", resp.Proto, alpn)
	}
}

// TestCookieRoundTrip proves two things at once: (1) the jar captures a
// server-issued Set-Cookie and resends it (the __cf_bm refresh mechanism), and
// (2) the stale __cf_bm we seeded is NOT replayed (change 4). httpbin echoes the
// cookies it received.
func TestCookieRoundTrip(t *testing.T) {
	skipUnlessFP(t)
	c := fpClient()

	// go-httpbin: /cookies/set 302-redirects to /cookies (followed by default),
	// which echoes the cookies the jar resent.
	status, body, err := c.doGET("https://httpbingo.org/cookies/set?fpcheck=42", c.token, c.headers)
	if err != nil {
		t.Skipf("httpbingo unreachable: %v", err)
	}
	if status != 200 {
		t.Skipf("httpbingo returned HTTP %d (echo unavailable): %s", status, snippet(body, 200))
	}
	t.Logf("httpbingo/cookies -> HTTP %d: %s", status, snippet(body, 300))

	// Shape-agnostic: both httpbin.org ({"cookies":{...}}) and go-httpbin
	// ({"fpcheck":["42"]}) embed the name+value; a round-trip just needs both.
	s := string(body)
	if !strings.Contains(s, "fpcheck") || !strings.Contains(s, "42") {
		t.Errorf("jar did not resend server-issued cookie: %s", snippet(body, 300))
	}
	if strings.Contains(s, "STALE_SHOULD_NOT_BE_SENT") {
		t.Errorf("stale __cf_bm was replayed but should have been skipped")
	}
}

// TestAcceptEncodingDecode proves that sending Accept-Encoding: gzip, deflate, br
// (change 2) still yields decodable JSON — tls-client transparently decompresses
// brotli and gzip, so doJSON's json.Unmarshal is unaffected.
func TestAcceptEncodingDecode(t *testing.T) {
	skipUnlessFP(t)
	c := fpClient()

	// Each codec is an independent subtest so an unavailable echo skips only
	// itself. gzip+deflate use go-httpbin (reliable); brotli uses httpbin.org
	// (best-effort — go-httpbin returns 501 for /brotli). A successful JSON
	// decode is itself the proof: had the codec not been transparently
	// decompressed, the body would be binary and json.Unmarshal would fail.
	cases := []struct {
		name, url, flag string
	}{
		{"gzip", "https://httpbingo.org/gzip", "gzipped"},
		{"deflate", "https://httpbingo.org/deflate", "deflated"},
		{"brotli", "https://httpbin.org/brotli", "brotli"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body, err := c.doGET(tc.url, c.token, c.headers)
			if err != nil {
				t.Skipf("%s unreachable: %v", tc.url, err)
			}
			if status != 200 {
				t.Skipf("%s returned HTTP %d (echo unavailable): %s", tc.url, status, snippet(body, 200))
			}
			var decoded map[string]any
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Errorf("%s: body did not decode (compression not handled?): %v (%s)", tc.url, err, snippet(body, 200))
				return
			}
			if v, ok := decoded[tc.flag].(bool); !ok || !v {
				t.Errorf("%s: expected %q:true in body, got %v", tc.url, tc.flag, decoded[tc.flag])
			}
			t.Logf("%s -> HTTP %d, decoded %s=%v", tc.url, status, tc.flag, decoded[tc.flag])
		})
	}
}
