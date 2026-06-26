package deliveroo

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// iosHeaderOrder is the HTTP/2 header order the Deliveroo iOS app sends on GETs
// (observed in captured requests). Lowercase per HTTP/2; headers not listed go
// after these. accept-encoding sits at index 5 (we now send it, see doGET).
var iosHeaderOrder = []string{
	"x-roo-app-version", "x-roo-country", "authorization", "x-roo-platform",
	"accept-language", "accept-encoding", "x-roo-rooblocks-version",
	"x-roo-sticky-guid", "accept", "user-agent", "x-roo-guid", "cookie",
}

// iosPostHeaderOrder is the app's HTTP/2 header order for JSON POSTs (observed in
// the captured /consumer/device-fingerprint and /orderapp/v1/session calls). It
// differs from the GET order: accept leads and content-type precedes cookie. The
// h2-managed/hop-by-hop headers (host, content-length, connection) are omitted.
var iosPostHeaderOrder = []string{
	"accept", "x-roo-app-version", "x-roo-country", "authorization",
	"x-roo-platform", "x-roo-sticky-guid", "accept-language",
	"x-roo-rooblocks-version", "accept-encoding", "user-agent",
	"x-roo-guid", "content-type", "cookie",
}

// iosPHeaderOrder is the HTTP/2 pseudo-header order an iOS (Apple Secure
// Transport) client sends. It matches the Safari_IOS profile's transport
// default; we set it explicitly so the akamai_fingerprint stays iOS-like
// regardless of profile changes.
var iosPHeaderOrder = []string{":method", ":scheme", ":authority", ":path"}

// iosProfile selects the iOS Safari TLS/HTTP-2 fingerprint. The Apple TLS stack
// is shared with the native app, so the JA3/JA4 matches an iPhone closely.
// Default is ios26 (closest to the captured iOS 27 device: TLS 1.3/1.2 only plus
// the X25519MLKEM768 post-quantum keyshare). Override via DELIVEROO_TLS_PROFILE
// (ios26 | ios18 | ios17).
func iosProfile() profiles.ClientProfile {
	switch os.Getenv("DELIVEROO_TLS_PROFILE") {
	case "ios18":
		return profiles.Safari_IOS_18_5
	case "ios17":
		return profiles.Safari_IOS_17_0
	case "ios26":
		return profiles.Safari_IOS_26_0
	default:
		return profiles.Safari_IOS_26_0
	}
}

// newIOSClient builds a tls-client that presents an iOS fingerprint, with a
// cookie jar so Cloudflare's __cf_bm refreshes across the (multi-session) pull.
func newIOSClient() (tls_client.HttpClient, error) {
	return tls_client.NewHttpClient(tls_client.NewNoopLogger(),
		tls_client.WithClientProfile(iosProfile()),
		tls_client.WithCookieJar(tls_client.NewCookieJar()),
		tls_client.WithTimeoutSeconds(30),
	)
}

// isCloudflareCookie reports whether a cookie is Cloudflare-issued and so must
// NOT be replayed from a stale capture. __cf_bm (~30-min TTL) and cf_clearance
// are bound to the original session/connection; presenting a stale one is more
// suspicious than presenting none. We let Cloudflare mint a fresh __cf_bm via
// Set-Cookie, which the jar then captures and resends on later requests.
func isCloudflareCookie(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return n == "__cf_bm" || n == "cf_clearance" || strings.HasPrefix(n, "__cf")
}

// seedCookies loads a captured "k=v; k=v" Cookie header into the tls-client jar
// for the API host, so the app's session cookies (roo_guid, roo_session_guid,
// roo_super_properties) are sent. Cloudflare cookies are deliberately skipped
// (see isCloudflareCookie) and instead refreshed from responses by the jar.
func (c *Client) seedCookies(cookieHeader string) {
	if c.tlsClient == nil || cookieHeader == "" {
		return
	}
	u, err := url.Parse(c.host)
	if err != nil {
		return
	}
	var cookies []*fhttp.Cookie
	for _, pair := range strings.Split(cookieHeader, ";") {
		kv := strings.SplitN(strings.TrimSpace(pair), "=", 2)
		if len(kv) != 2 || kv[0] == "" {
			continue
		}
		name := strings.TrimSpace(kv[0])
		if isCloudflareCookie(name) {
			continue // skip stale CF cookies; the jar captures a fresh __cf_bm
		}
		cookies = append(cookies, &fhttp.Cookie{Name: name, Value: strings.TrimSpace(kv[1])})
	}
	if len(cookies) > 0 {
		c.tlsClient.SetCookies(u, cookies)
	}
}

// doGET issues a GET through the iOS-fingerprinted client, replaying the captured
// header block in app order. Cookies come from the jar (not the header). Returns
// the status and body. Falls back to the stdlib client if tls-client is absent.
func (c *Client) doGET(reqURL, token string, headers map[string]string) (int, []byte, error) {
	if c.tlsClient == nil {
		return c.doGETStdlib(reqURL, token, headers)
	}
	req, err := fhttp.NewRequest("GET", reqURL, nil)
	if err != nil {
		return 0, nil, err
	}
	for k, v := range headers {
		lk := strings.ToLower(k)
		if headersToSkip[lk] || lk == "cookie" { // cookie handled by the jar
			continue
		}
		req.Header.Set(k, v)
	}
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	// The app sends Accept-Encoding: gzip, deflate, br (it's in the captured
	// header block). tls-client transparently decompresses the response, so
	// json decoding is unaffected. Defensive fallback if the capture lacked it.
	if req.Header.Get("Accept-Encoding") == "" {
		req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	}
	req.Header[fhttp.HeaderOrderKey] = iosHeaderOrder
	req.Header[fhttp.PHeaderOrderKey] = iosPHeaderOrder

	resp, err := c.tlsClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 6<<20))
	return resp.StatusCode, body, err
}

// doPOST issues a JSON POST through the iOS-fingerprinted client, replaying the
// captured header block in the app's POST order (distinct from GETs) with the
// iOS pseudo-header order. Cookies come from the jar. It refuses the stdlib
// fallback: a POST without the iOS fingerprint defeats the purpose (warmup).
func (c *Client) doPOST(reqURL, token string, headers map[string]string, body []byte) (int, []byte, error) {
	if c.tlsClient == nil {
		return 0, nil, fmt.Errorf("doPOST requires the iOS tls-client (stdlib fallback not supported)")
	}
	req, err := fhttp.NewRequest("POST", reqURL, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	for k, v := range headers {
		lk := strings.ToLower(k)
		if headersToSkip[lk] || lk == "cookie" || lk == "content-type" {
			continue // cookie via jar; content-type set explicitly below
		}
		req.Header.Set(k, v)
	}
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	req.Header.Set("Content-Type", "application/json")
	if req.Header.Get("Accept-Encoding") == "" {
		req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	}
	req.Header[fhttp.HeaderOrderKey] = iosPostHeaderOrder
	req.Header[fhttp.PHeaderOrderKey] = iosPHeaderOrder

	resp, err := c.tlsClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, err
}

// doGETStdlib is the fallback path (Go TLS fingerprint) used only if the
// iOS-fingerprinted client failed to build.
func (c *Client) doGETStdlib(reqURL, token string, headers map[string]string) (int, []byte, error) {
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return 0, nil, err
	}
	setIOSAppHeaders(req, token, headers)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 6<<20))
	return resp.StatusCode, body, err
}
