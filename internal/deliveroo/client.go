package deliveroo

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	tls_client "github.com/bogdanfinn/tls-client"

	"deliveroo-wrapped/internal/models"
)

// defaultHost is the Deliveroo consumer API host for the UK app. Other markets
// use a different subdomain (co-m.<tld>.deliveroo.com); override via the captured
// request if needed.
const defaultHost = "https://co-m.uk.deliveroo.com"

// ordersPageLimit matches the app's page size for the order-history endpoint.
const ordersPageLimit = 25

// default human-like delays between requests (restored after enrichment pacing).
const (
	defaultMinDelay = 800 * time.Millisecond
	defaultMaxDelay = 2500 * time.Millisecond
)

// headersToSkip are captured headers we must NOT replay verbatim:
//   - Host/Content-Length: managed by net/http from the request itself.
//   - Accept-Encoding: if we set it manually, Go won't transparently decompress
//     the (gzip/br) response and json decoding fails. Let the transport handle it.
//   - If-Modified-Since/If-None-Match: would yield a 304 with an empty body.
//   - Connection: hop-by-hop.
var headersToSkip = map[string]bool{
	"host":              true,
	"content-length":    true,
	"accept-encoding":   true,
	"if-modified-since": true,
	"if-none-match":     true,
	"connection":        true,
}

// Client handles all Deliveroo API interactions with human-like throttling.
type Client struct {
	httpClient  *http.Client          // stdlib fallback + image fetch (CDN)
	tlsClient   tls_client.HttpClient // iOS-fingerprinted client for the API host
	jar         *cookiejar.Jar
	host        string
	token       string            // full Authorization header value, e.g. "Basic ..."
	userID      string            // derived from the Basic credential; needed for the detail path
	headers     map[string]string // verbatim captured iOS-app headers (minus Authorization)
	lastRequest time.Time
	mu          sync.Mutex
	minDelay    time.Duration
	maxDelay    time.Duration
}

// NewClient creates a new Deliveroo API client.
func NewClient() *Client {
	jar, _ := cookiejar.New(nil)
	c := &Client{
		jar:     jar,
		host:    defaultHost,
		headers: map[string]string{},
		httpClient: &http.Client{
			Jar:     jar,
			Timeout: 30 * time.Second,
		},
		minDelay: defaultMinDelay,
		maxDelay: defaultMaxDelay,
	}
	// Build the iOS-fingerprinted client for API calls. On failure we log and
	// fall back to the stdlib client (Go fingerprint) so the app still runs.
	if tc, err := newIOSClient(); err != nil {
		log.Printf("iOS TLS client unavailable, falling back to stdlib: %v", err)
	} else {
		c.tlsClient = tc
	}
	return c
}

// throttle waits a randomized human-like delay before the next request.
func (c *Client) throttle() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.lastRequest.IsZero() {
		elapsed := time.Since(c.lastRequest)
		randomDelay := c.minDelay + time.Duration(rand.Int63n(int64(c.maxDelay-c.minDelay)))
		if elapsed < randomDelay {
			time.Sleep(randomDelay - elapsed)
		}
	}
	c.lastRequest = time.Now()
}

// SetThrottling customizes the delay between requests.
func (c *Client) SetThrottling(min, max time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.minDelay = min
	c.maxDelay = max
}

// ResetThrottling restores the default human-like delay (used after the slow
// enrichment pacing).
func (c *Client) ResetThrottling() { c.SetThrottling(defaultMinDelay, defaultMaxDelay) }

// SetAuth stores the Authorization value and the captured iOS-app header block,
// deriving the numeric user ID from the Basic credential (needed for the order
// detail path). The header block is replayed verbatim as the app fingerprint.
func (c *Client) SetAuth(token string, headers map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = token
	if headers == nil {
		headers = map[string]string{}
	}
	c.headers = headers
	c.userID = userIDFromAuth(token)
	// Seed the captured session cookies into the iOS client's jar so they're
	// sent and __cf_bm refreshes across the pull.
	for k, v := range headers {
		if strings.EqualFold(k, "Cookie") {
			c.seedCookies(v)
			break
		}
	}
}

// GetToken returns the current Authorization value.
func (c *Client) GetToken() string { return c.token }

// GetHeaders returns the captured header block.
func (c *Client) GetHeaders() map[string]string { return c.headers }

// UserID returns the numeric user ID derived from the auth credential.
func (c *Client) UserID() string { return c.userID }

// userIDFromAuth decodes a "Basic base64(userID:orderapp_ios,<jwt>)" credential
// and returns the userID. Returns "" if the credential isn't in that form.
func userIDFromAuth(auth string) string {
	const prefix = "Basic "
	if !strings.HasPrefix(auth, prefix) {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(auth[len(prefix):]))
	if err != nil {
		return ""
	}
	if i := strings.IndexByte(string(raw), ':'); i > 0 {
		return string(raw)[:i]
	}
	return ""
}

// SetHost overrides the API host (e.g. for a non-UK capture). No-op on "".
func (c *Client) SetHost(host string) {
	if host == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.host = host
}

// setIOSAppHeaders replays the captured iOS-app fingerprint (minus problematic
// headers, see headersToSkip), then applies the Authorization token. token and
// headers are snapshotted by the caller under c.mu to avoid racing with SetAuth.
func setIOSAppHeaders(req *http.Request, token string, headers map[string]string) {
	for k, v := range headers {
		if headersToSkip[strings.ToLower(k)] {
			continue
		}
		req.Header.Set(k, v)
	}
	if token != "" {
		req.Header.Set("Authorization", token)
	}
}

// isRooCDNHost reports whether host is Deliveroo's image CDN.
func isRooCDNHost(host string) bool {
	host = strings.ToLower(host)
	return host == "roocdn.com" || strings.HasSuffix(host, ".roocdn.com")
}

// headerCI returns a captured header by case-insensitive name.
func (c *Client) headerCI(name string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range c.headers {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// FetchImage downloads an image (e.g. a restaurant logo) from the CDN, spoofing
// the iOS app fingerprint (User-Agent + Accept-Language) so it looks like the
// app. It deliberately does NOT send the API Authorization/Cookie, which belong
// to the API host, not the image CDN. Returns the content type and bytes.
func (c *Client) FetchImage(imgURL string) (string, []byte, error) {
	// Defense-in-depth: only fetch from Deliveroo's image CDN, even though the
	// URL originates from an authenticated API response.
	u, err := url.Parse(imgURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || !isRooCDNHost(u.Hostname()) {
		return "", nil, fmt.Errorf("refusing to fetch non-CDN image URL")
	}
	req, err := http.NewRequest("GET", imgURL, nil)
	if err != nil {
		return "", nil, err
	}
	ua := c.headerCI("User-Agent")
	if ua == "" {
		ua = "Deliveroo-OrderApp/3.328.0 (iPhone; iOS 17; Scale/3.00)"
	}
	req.Header.Set("User-Agent", ua)
	if al := c.headerCI("Accept-Language"); al != "" {
		req.Header.Set("Accept-Language", al)
	}
	req.Header.Set("Accept", "image/webp,image/jpeg,image/png,image/*,*/*;q=0.8")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("unexpected status %d fetching image", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20)) // 5 MB cap
	if err != nil {
		return "", nil, err
	}
	return resp.Header.Get("Content-Type"), data, nil
}

func (c *Client) doJSON(reqURL string, out interface{}) error {
	c.throttle()
	c.mu.Lock()
	token, headers := c.token, c.headers
	c.mu.Unlock()

	status, body, err := c.doGET(reqURL, token, headers)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("unexpected status %d: %s", status, snippet(body, 300))
	}
	if err := json.Unmarshal(body, out); err != nil {
		// Include a body snippet: a Cloudflare challenge/HTML page can be served
		// with a 200, and block-detection needs to see it (the bare decode error
		// carries no signal otherwise).
		return fmt.Errorf("decoding response (%v): %s", err, snippet(body, 300))
	}
	return nil
}

// snippet returns up to n bytes of b as a string, trimmed to a valid UTF-8
// boundary so a split rune doesn't render as a replacement char.
func snippet(b []byte, n int) string {
	if len(b) > n {
		b = b[:n]
	}
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return string(b)
}

// GetOrders fetches one page of order history at the given offset.
func (c *Client) GetOrders(offset int) (*models.OrderListResponse, error) {
	reqURL := fmt.Sprintf("%s/consumer/order-history/v1/orders?limit=%d&offset=%d&include_ugc=true",
		c.host, ordersPageLimit, offset)
	var listResp models.OrderListResponse
	if err := c.doJSON(reqURL, &listResp); err != nil {
		return nil, err
	}
	return &listResp, nil
}

// GetAllOrders paginates the full order history via offset/limit, stopping when
// a page returns fewer than a full page of orders.
func (c *Client) GetAllOrders(progressFn func(count int)) ([]models.APIOrder, error) {
	var all []models.APIOrder
	for offset := 0; ; offset += ordersPageLimit {
		resp, err := c.GetOrders(offset)
		if err != nil {
			return nil, err
		}
		all = append(all, resp.Orders...)
		if progressFn != nil {
			progressFn(len(all))
		}
		if len(resp.Orders) < ordersPageLimit {
			break
		}
	}
	return all, nil
}

// GetOrderDetails fetches one order's detail (service fee + restaurant coords),
// which the list endpoint omits.
func (c *Client) GetOrderDetails(orderID string) (*models.OrderDetailResponse, error) {
	if c.userID == "" {
		return nil, fmt.Errorf("no user ID available (auth credential not in expected Basic form)")
	}
	reqURL := fmt.Sprintf("%s/orderapp/v1/users/%s/orders/%s", c.host, c.userID, orderID)
	var d models.OrderDetailResponse
	if err := c.doJSON(reqURL, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// GetUser fetches the account profile (name + Plus subscription).
func (c *Client) GetUser() (*models.APIUser, error) {
	if c.userID == "" {
		return nil, fmt.Errorf("no user ID available (auth credential not in expected Basic form)")
	}
	reqURL := fmt.Sprintf("%s/orderapp/v1/users/%s", c.host, c.userID)
	var user models.APIUser
	if err := c.doJSON(reqURL, &user); err != nil {
		return nil, err
	}
	return &user, nil
}
