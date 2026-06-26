package deliveroo

import (
	"io"
	"os"
	"testing"

	fhttp "github.com/bogdanfinn/fhttp"
)

// TestIOSFingerprint hits a TLS-fingerprint echo service through the
// iOS-fingerprinted client and prints its JA3/JA4 + HTTP-2 fingerprint. It's a
// one-off sanity check (network), skipped unless DELIVEROO_FP_TEST=1 — never run
// in CI and never touches Deliveroo. Eyeball the output: ja3/ja4/akamai should
// look like an iPhone, not Go's default.
func TestIOSFingerprint(t *testing.T) {
	if os.Getenv("DELIVEROO_FP_TEST") == "" {
		t.Skip("set DELIVEROO_FP_TEST=1 to run the live fingerprint echo check")
	}
	c, err := newIOSClient()
	if err != nil {
		t.Fatalf("newIOSClient: %v", err)
	}
	req, err := fhttp.NewRequest("GET", "https://tls.peet.ws/api/all", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("User-Agent", "Deliveroo-OrderApp/3.328.0 (iPhone18,4; iOS27.0; Release; en_US; 530840)")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header[fhttp.HeaderOrderKey] = iosHeaderOrder

	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	t.Logf("HTTP %d\n%s", resp.StatusCode, string(b))
}
