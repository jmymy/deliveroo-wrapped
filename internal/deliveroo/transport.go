package deliveroo

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// iosHeaderOrder is the HTTP/2 header order the Deliveroo iOS app sends (observed
// in captured requests). Lowercase per HTTP/2; headers not listed go after these.
var iosHeaderOrder = []string{
	"x-roo-app-version", "x-roo-country", "authorization", "x-roo-platform",
	"accept-language", "accept-encoding", "x-roo-rooblocks-version",
	"x-roo-sticky-guid", "accept", "user-agent", "x-roo-guid", "cookie",
}

// iosProfile selects the iOS Safari TLS/HTTP-2 fingerprint. The Apple TLS stack
// is shared with the native app, so the JA3/JA4 matches an iPhone closely.
// Override via DELIVEROO_TLS_PROFILE (ios18 | ios26 | ios17).
func iosProfile() profiles.ClientProfile {
	switch os.Getenv("DELIVEROO_TLS_PROFILE") {
	case "ios26":
		return profiles.Safari_IOS_26_0
	case "ios17":
		return profiles.Safari_IOS_17_0
	default:
		return profiles.Safari_IOS_18_5
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

// seedCookies loads a captured "k=v; k=v" Cookie header into the tls-client jar
// for the API host, so the app's session cookies (roo_guid, roo_session_guid,
// roo_super_properties, __cf_bm) are sent and later refreshed from responses.
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
		if len(kv) == 2 && kv[0] != "" {
			cookies = append(cookies, &fhttp.Cookie{Name: strings.TrimSpace(kv[0]), Value: strings.TrimSpace(kv[1])})
		}
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
	req.Header[fhttp.HeaderOrderKey] = iosHeaderOrder

	resp, err := c.tlsClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 6<<20))
	return resp.StatusCode, body, err
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
