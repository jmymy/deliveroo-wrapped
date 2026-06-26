package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"deliveroo-wrapped/internal/deliveroo"
	"deliveroo-wrapped/internal/models"
	"deliveroo-wrapped/internal/stats"
	"deliveroo-wrapped/internal/storage"
)

//go:embed templates/*
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

const (
	defaultPlusMonthly      = 3.49 // GBP/month, Deliveroo Plus (fallback)
	defaultBaselineDelivery = 2.99 // assumed pre-Plus delivery fee (estimate)
)

// offerPriceRe pulls the pence figure out of an offer_uname like
// "uk_monthly_2499_2025Q2_no_trial".
var offerPriceRe = regexp.MustCompile(`_(\d{3,})_`)

type Server struct {
	store     *storage.Storage
	client    *deliveroo.Client
	templates *template.Template
	data      *models.DataStore
	auth      *models.AuthState
	logoDir   string
	mu        sync.RWMutex

	syncInProgress bool
	syncStatus     string
	syncProgress   int
	syncTotal      int
}

func main() {
	dataDir := os.Getenv("DELIVEROO_DATA_DIR")
	if dataDir == "" {
		homeDir, _ := os.UserHomeDir()
		dataDir = filepath.Join(homeDir, ".deliveroostats")
	}

	store, err := storage.New(dataDir)
	if err != nil {
		log.Fatalf("Failed to create storage: %v", err)
	}

	data, err := store.LoadData()
	if err != nil {
		log.Fatalf("Failed to load data: %v", err)
	}
	if data.PlusMonthlyCost == 0 {
		data.PlusMonthlyCost = plusMonthlyFromEnv()
	}
	if data.BaselineDeliveryFee == 0 {
		data.BaselineDeliveryFee = baselineDeliveryFromEnv()
	}

	// Dev seed: populate synthetic orders so the dashboard is demoable before a
	// real sync. Only fires when DELIVEROO_SEED=1 and there's no real data.
	if os.Getenv("DELIVEROO_SEED") == "1" && len(data.Orders) == 0 {
		data.Orders = seedOrders()
		data.UserName = "Demo Eater"
		log.Printf("Loaded %d seed orders (DELIVEROO_SEED=1)", len(data.Orders))
	}

	// Offline ingest: load a saved order-history JSON page (one captured response
	// body) through the adapter instead of hitting the API. Useful for testing.
	if f := os.Getenv("DELIVEROO_IMPORT_FILE"); f != "" {
		n, err := importOrdersFile(store, data, f)
		if err != nil {
			log.Printf("Import from %s failed: %v", f, err)
		} else {
			log.Printf("Imported %d orders from %s", n, f)
		}
	}

	auth, err := store.LoadAuth()
	if err != nil {
		log.Fatalf("Failed to load auth: %v", err)
	}

	tmpl, err := template.New("").Funcs(funcMap()).ParseFS(templatesFS, "templates/*.html", "templates/partials/*.html")
	if err != nil {
		log.Fatalf("Failed to parse templates: %v", err)
	}

	client := deliveroo.NewClient()
	if auth.Token != "" {
		client.SetAuth(auth.Token, auth.Headers)
	}
	if auth.Host != "" {
		client.SetHost(auth.Host)
	}

	logoDir := filepath.Join(dataDir, "logos")
	if err := os.MkdirAll(logoDir, 0700); err != nil {
		log.Printf("creating logo cache dir: %v", err)
	}

	server := &Server{
		store:     store,
		client:    client,
		templates: tmpl,
		data:      data,
		auth:      auth,
		logoDir:   logoDir,
	}

	mux := http.NewServeMux()
	mux.Handle("/static/", http.FileServer(http.FS(staticFS)))
	mux.HandleFunc("/", server.handleHub)
	mux.HandleFunc("/story", server.handleStory)
	mux.HandleFunc("/explore", server.handleExplore)
	mux.HandleFunc("/cards", server.handleCards)
	mux.HandleFunc("/share", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/cards", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/auth", server.handleAuth)
	mux.HandleFunc("/api/manual-auth", server.handleManualAuth)
	mux.HandleFunc("/api/logout", server.handleLogout)
	mux.HandleFunc("/api/sync", server.handleSync)
	mux.HandleFunc("/api/enrich", server.handleEnrich)
	mux.HandleFunc("/api/sync-status", server.handleSyncStatus)
	mux.HandleFunc("/api/stats", server.handleStats)
	mux.HandleFunc("/api/order-locations", server.handleOrderLocations)
	mux.HandleFunc("/api/logo", server.handleLogo)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	// Bind loopback by default: the dashboard + endpoints expose personal data
	// (orders, home delivery coords) and the auth token with no authentication.
	// Override with DELIVEROO_BIND only if you know what you're doing.
	bind := os.Getenv("DELIVEROO_BIND")
	if bind == "" {
		bind = "127.0.0.1"
	}
	addr := bind + ":" + port
	log.Printf("Starting Deliveroo Wrapped on http://%s:%s", "localhost", port)
	log.Printf("Data directory: %s", dataDir)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

// blockCrossSite rejects state-changing requests that a browser marks as
// cross-site (a basic CSRF guard for the unauthenticated local POST endpoints).
func (s *Server) blockCrossSite(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		http.Error(w, "cross-site request blocked", http.StatusForbidden)
		return true
	}
	return false
}

// importOrdersFile ingests a saved order-history JSON page through the adapter.
func importOrdersFile(store *storage.Storage, data *models.DataStore, path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var page models.OrderListResponse
	if err := json.Unmarshal(raw, &page); err != nil {
		return 0, err
	}
	added := 0
	for _, o := range page.Orders {
		if !store.OrderExists(data, o.ID) {
			store.AddOrderFromAPI(data, o, data.BaselineDeliveryFee)
			added++
		}
	}
	return added, nil
}

// deriveHost extracts scheme://host from a captured request URL, falling back to
// the captured "Host" header. Returns "" if neither is present.
func deriveHost(rawURL string, headers map[string]string) string {
	if rawURL != "" {
		if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
			scheme := u.Scheme
			if scheme == "" {
				scheme = "https"
			}
			return scheme + "://" + u.Host
		}
	}
	for k, v := range headers {
		if strings.EqualFold(k, "Host") && v != "" {
			return "https://" + v
		}
	}
	return ""
}

func plusMonthlyFromEnv() float64 {
	if v := os.Getenv("DELIVEROO_PLUS_MONTHLY"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			return f
		}
	}
	return defaultPlusMonthly
}

func baselineDeliveryFromEnv() float64 {
	if v := os.Getenv("DELIVEROO_BASELINE_DELIVERY"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			return f
		}
	}
	return defaultBaselineDelivery
}

// plusMonthlyFromOffer parses a price from a Deliveroo offer_uname such as
// "uk_monthly_2499_2025Q2_no_trial" → 24.99. Returns 0 if no price is found.
func plusMonthlyFromOffer(offerUname string) float64 {
	m := offerPriceRe.FindStringSubmatch(offerUname)
	if m == nil {
		return 0
	}
	pence, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return float64(pence) / 100.0
}

func funcMap() template.FuncMap {
	return template.FuncMap{
		"formatDate":     func(t time.Time) string { return t.Format("Jan 2, 2006") },
		"formatDateTime": func(t time.Time) string { return t.Format("Jan 2, 2006 3:04 PM") },
		"formatMoney":    formatMoney,
		"formatMinutes": func(m float64) string {
			if m >= 60 {
				return fmt.Sprintf("%dh %dm", int(m)/60, int(m)%60)
			}
			return fmt.Sprintf("%.0f min", m)
		},
		"formatFloat": func(f float64, decimals int) string {
			return fmt.Sprintf(fmt.Sprintf("%%.%df", decimals), f)
		},
		"absf": func(f float64) float64 {
			if f < 0 {
				return -f
			}
			return f
		},
		"monthName": stats.MonthName,
		"dayName":   stats.DayName,
		"json": func(v interface{}) template.JS {
			b, _ := json.Marshal(v)
			return template.JS(b)
		},
		"add": func(a, b int) int { return a + b },
		"toF": func(i int) float64 { return float64(i) },
		"divf": func(a, b float64) float64 {
			if b == 0 {
				return 0
			}
			return a / b
		},
		"monogram":  monogram,
		"restColor": restColor,
		"dayMonth":  dayMonth,
		// Year-over-year deltas (current vs previous).
		"signedInt": func(cur, prev int) string {
			d := cur - prev
			if d > 0 {
				return fmt.Sprintf("+%d", d)
			}
			return fmt.Sprintf("%d", d)
		},
		"signedMoney": func(cur, prev float64, currency string) string {
			d := cur - prev
			sign := "+"
			if d < 0 {
				sign = "-"
				d = -d
			} else if d == 0 {
				sign = ""
			}
			return sign + formatMoney(d, currency)
		},
		// deltaClass returns up/down/flat for coloring. higherIsBetter flips the
		// semantics (e.g. spending more isn't "good", so pass false there).
		"deltaClass": func(cur, prev float64, higherIsBetter bool) string {
			if cur == prev {
				return "flat"
			}
			up := cur > prev
			if !higherIsBetter {
				up = !up
			}
			if up {
				return "up"
			}
			return "down"
		},
	}
}

func formatMoney(amount float64, currency string) string {
	symbol := "£"
	switch currency {
	case "USD":
		symbol = "$"
	case "EUR":
		symbol = "€"
	}
	return fmt.Sprintf("%s%.2f", symbol, amount)
}

func (s *Server) yearFromQuery(r *http.Request) int {
	yearStr := r.URL.Query().Get("year")
	year := time.Now().Year()
	if yearStr == "all" {
		return 0
	}
	if yearStr != "" {
		if y, err := strconv.Atoi(yearStr); err == nil && y > 2000 && y <= 2100 {
			return y
		}
	}
	// Default to the most recent year with data, else current year.
	if years := s.store.GetAvailableYears(s.data); len(years) > 0 {
		return years[0]
	}
	return year
}

func (s *Server) ordersForYear(year int) []models.StoredOrder {
	if year == 0 {
		return s.data.Orders
	}
	return s.store.GetOrdersForYear(s.data, year)
}

// monogram returns 1-2 uppercase initials for a restaurant avatar.
func monogram(name string) string {
	fields := strings.Fields(strings.TrimSpace(name))
	if len(fields) == 0 {
		return "?"
	}
	r := []rune(fields[0])
	out := strings.ToUpper(string(r[0]))
	if len(fields) > 1 {
		r2 := []rune(fields[1])
		out += strings.ToUpper(string(r2[0]))
	} else if len(r) > 1 {
		out += strings.ToUpper(string(r[1]))
	}
	return out
}

var restPalette = []string{"#FF5E5B", "#FFB400", "#7B61FF", "#FF8A00", "#00A99D", "#00CCBC"}

// restColor returns a round-robin avatar background for leaderboard index i.
func restColor(i int) string { return restPalette[i%len(restPalette)] }

// dayMonth formats a "2006-01-02" date string as "2 Jan"; passes through on error.
func dayMonth(s string) string {
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.Format("2 Jan")
	}
	return s
}

// pageCtx holds the year + computed stats shared by every page handler.
type pageCtx struct {
	Year           int
	AvailableYears []int
	Stats          *models.YearlyStats
	PrevStats      *models.YearlyStats
	PrevYear       int
	HasPrev        bool
	HasData        bool
}

// pageCtx computes the per-request year stats + prior-year comparison. Caller
// must hold s.mu.RLock.
func (s *Server) buildPageCtx(r *http.Request) pageCtx {
	year := s.yearFromQuery(r)
	orders := s.ordersForYear(year)
	yearStats := stats.Calculate(orders, year, s.data.PlusMonthlyCost)
	avail := s.store.GetAvailableYears(s.data)

	var prevStats *models.YearlyStats
	prevYear := year - 1
	if year != 0 {
		for _, y := range avail {
			if y == prevYear {
				prevStats = stats.Calculate(s.ordersForYear(prevYear), prevYear, s.data.PlusMonthlyCost)
				break
			}
		}
	}
	return pageCtx{
		Year: year, AvailableYears: avail,
		Stats: yearStats, PrevStats: prevStats, PrevYear: prevYear,
		HasPrev: prevStats != nil && prevStats.TotalOrders > 0,
		HasData: yearStats.TotalOrders > 0,
	}
}

// viewModel is the JSON injected for the explore charts + map.
type viewModel struct {
	SpendByMonth [12]float64           `json:"spendByMonth"`
	OrdersByDow  [7]int                `json:"ordersByDow"` // Mon..Sun
	Destinations []models.AddressEntry `json:"destinations"`
}

func buildViewModel(st *models.YearlyStats) viewModel {
	var vm viewModel
	for m := 1; m <= 12; m++ {
		vm.SpendByMonth[m-1] = st.SpendByMonth[m]
	}
	// stats uses 0=Sunday..6=Saturday; the chart wants Mon..Sun.
	order := []int{1, 2, 3, 4, 5, 6, 0}
	for i, d := range order {
		vm.OrdersByDow[i] = st.OrdersByDayOfWeek[d]
	}
	vm.Destinations = st.TopAddresses
	return vm
}

// baseData seeds the common template keys every page uses.
func (s *Server) baseData(ctx pageCtx) map[string]interface{} {
	return map[string]interface{}{
		"Auth":           s.auth,
		"Stats":          ctx.Stats,
		"PrevStats":      ctx.PrevStats,
		"PrevYear":       ctx.PrevYear,
		"HasPrev":        ctx.HasPrev,
		"HasData":        ctx.HasData,
		"UserName":       s.data.UserName,
		"PlusTier":       s.data.PlusTier,
		"MemberSince":    s.data.MemberSince,
		"SelectedYear":   ctx.Year,
		"AvailableYears": ctx.AvailableYears,
		"LoggedIn":       s.auth.LoggedIn,
		"SyncInProgress": s.syncInProgress,
	}
}

func (s *Server) handleHub(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ctx := s.buildPageCtx(r)
	if !ctx.HasData {
		s.renderTemplate(w, "empty.html", s.baseData(ctx))
		return
	}
	s.renderTemplate(w, "hub.html", s.baseData(ctx))
}

func (s *Server) handleStory(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ctx := s.buildPageCtx(r)
	if !ctx.HasData {
		s.renderTemplate(w, "empty.html", s.baseData(ctx))
		return
	}
	s.renderTemplate(w, "story.html", s.baseData(ctx))
}

func (s *Server) handleExplore(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ctx := s.buildPageCtx(r)
	if !ctx.HasData {
		s.renderTemplate(w, "empty.html", s.baseData(ctx))
		return
	}
	data := s.baseData(ctx)
	data["ViewModel"] = buildViewModel(ctx.Stats)
	s.renderTemplate(w, "explore.html", data)
}

func (s *Server) handleCards(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ctx := s.buildPageCtx(r)
	if !ctx.HasData {
		s.renderTemplate(w, "empty.html", s.baseData(ctx))
		return
	}
	data := s.baseData(ctx)
	if len(ctx.Stats.TopRestaurants) > 0 {
		data["TopRestaurant"] = &ctx.Stats.TopRestaurants[0]
	}
	s.renderTemplate(w, "cards.html", data)
}

func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	s.renderTemplate(w, "auth.html", map[string]interface{}{"Auth": s.auth})
}

// handleManualAuth accepts a pasted "Copy as cURL" command (or a raw header
// block) captured from the iOS app, parses the headers + bearer token, and
// stores them for token replay.
func (s *Server) handleManualAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.blockCrossSite(w, r) {
		return
	}
	pasted := r.FormValue("curl")
	tokenOnly := r.FormValue("token")
	userName := r.FormValue("user_name")

	parsed := deliveroo.ParseCurl(pasted)
	token := parsed.Token
	headers := parsed.Headers
	if tokenOnly != "" { // allow refreshing just the token, keep existing headers
		token = tokenOnly
		if len(headers) == 0 {
			s.mu.RLock()
			headers = s.auth.Headers
			s.mu.RUnlock()
		}
	}
	if token == "" {
		s.renderHTMXError(w, "No Authorization token found. Paste the full 'Copy as cURL' from your proxy, or paste a fresh token in the token field.")
		return
	}

	host := deriveHost(parsed.URL, headers)
	s.client.SetAuth(token, headers)
	s.client.SetHost(host)

	s.mu.Lock()
	s.auth.Token = token
	s.auth.Headers = headers
	if host != "" {
		s.auth.Host = host
	}
	if userName != "" {
		s.auth.UserName = userName
	} else if s.auth.UserName == "" {
		s.auth.UserName = "Deliveroo User"
	}
	s.auth.LoggedIn = true
	if err := s.store.SaveAuth(s.auth); err != nil {
		log.Printf("Failed to save auth: %v", err)
	}
	s.mu.Unlock()

	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if s.blockCrossSite(w, r) {
		return
	}
	s.mu.Lock()
	s.auth = &models.AuthState{Headers: map[string]string{}}
	s.client.SetAuth("", nil)
	if err := s.store.SaveAuth(s.auth); err != nil {
		log.Printf("save auth: %v", err)
	}
	s.mu.Unlock()
	w.Header().Set("HX-Redirect", "/auth")
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.blockCrossSite(w, r) {
		return
	}
	s.mu.Lock()
	if s.syncInProgress {
		s.mu.Unlock()
		s.renderHTMXError(w, "Sync already in progress")
		return
	}
	s.syncInProgress = true
	s.syncStatus = "Starting sync..."
	s.syncProgress = 0
	s.syncTotal = 0
	s.mu.Unlock()

	go s.performSync()

	s.renderTemplate(w, "sync-status.html", map[string]interface{}{
		"Status": "Starting sync...", "Progress": 0, "Total": 0, "ProgressPct": 0.0, "InProgress": true,
	})
}

// handleEnrich starts the opt-in detail-enrichment pass (one detail call per
// delivered, not-yet-enriched order: service fee + restaurant coords). Separate
// from sync so the heavy call volume is an explicit choice. Resumable.
func (s *Server) handleEnrich(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.blockCrossSite(w, r) {
		return
	}
	s.mu.Lock()
	if s.syncInProgress {
		s.mu.Unlock()
		s.renderHTMXError(w, "A sync/enrich is already in progress")
		return
	}
	s.syncInProgress = true
	s.syncStatus = "Starting enrichment..."
	s.syncProgress = 0
	s.syncTotal = 0
	s.mu.Unlock()

	go s.performEnrich()

	s.renderTemplate(w, "sync-status.html", map[string]interface{}{
		"Status": "Starting enrichment...", "Progress": 0, "Total": 0, "ProgressPct": 0.0, "InProgress": true,
	})
}

func (s *Server) performEnrich() {
	defer func() {
		s.mu.Lock()
		s.syncInProgress = false
		s.mu.Unlock()
	}()
	s.runEnrichment()
}

// runEnrichment fills service fee + restaurant coords for delivered, not-yet-
// enriched orders (one detail call each). Resumable; stops cleanly on auth
// error so a fresh token can resume it.
func (s *Server) runEnrichment() {
	s.mu.RLock()
	var ids []string
	for _, o := range s.data.Orders {
		if !o.Enriched && o.Status == "DELIVERED" {
			ids = append(ids, o.ID)
		}
	}
	s.mu.RUnlock()

	total := len(ids)
	if total == 0 {
		s.updateSyncStatus("Nothing to enrich — all delivered orders already have fee details.", 0, 0)
		return
	}

	done := 0
	for i, id := range ids {
		s.updateSyncStatus(fmt.Sprintf("Enriching fees %d/%d...", i+1, total), i+1, total)
		d, err := s.client.GetOrderDetails(id)
		if err != nil {
			if isAuthError(err) {
				s.mu.Lock()
				s.save()
				s.mu.Unlock()
				s.updateSyncStatus(fmt.Sprintf("Token expired after %d/%d. Re-paste a fresh token at /auth, then click Enrich to resume.", done, total), done, total)
				return
			}
			log.Printf("enrich %s failed: %v", id, err)
			continue
		}
		s.mu.Lock()
		if s.store.EnrichOrderFromDetail(s.data, id, d) {
			done++
		}
		if (i+1)%25 == 0 {
			s.save()
		}
		s.mu.Unlock()
	}

	s.mu.Lock()
	s.save()
	s.mu.Unlock()
	s.updateSyncStatus(fmt.Sprintf("Enrichment complete! Filled fees for %d orders.", done), total, total)
}

func isAuthError(err error) bool {
	m := err.Error()
	return strings.Contains(m, "status 401") || strings.Contains(m, "status 403")
}

func (s *Server) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	progressPct := 0.0
	if s.syncTotal > 0 {
		progressPct = float64(s.syncProgress) * 100.0 / float64(s.syncTotal)
	}
	s.renderTemplate(w, "sync-status.html", map[string]interface{}{
		"Status": s.syncStatus, "Progress": s.syncProgress, "Total": s.syncTotal,
		"ProgressPct": progressPct, "InProgress": s.syncInProgress,
	})
}

func (s *Server) performSync() {
	defer func() {
		s.mu.Lock()
		s.syncInProgress = false
		s.mu.Unlock()
	}()

	// Fetch the account profile first: name + Plus tier + Plus price.
	s.updateSyncStatus("Fetching your profile...", 0, 0)
	if user, err := s.client.GetUser(); err != nil {
		log.Printf("Failed to fetch user profile (continuing): %v", err)
	} else {
		s.mu.Lock()
		if user.PreferredName != "" {
			s.data.UserName = user.PreferredName
		} else if user.FullName != "" {
			s.data.UserName = user.FullName
		}
		s.data.PlusTier = user.Subscription.SubscriptionTier
		if p := plusMonthlyFromOffer(user.Subscription.OfferUname); p > 0 {
			s.data.PlusMonthlyCost = p
		}
		if t, err := time.Parse(time.RFC3339, user.Created); err == nil {
			s.data.MemberSince = t.Year()
		}
		s.mu.Unlock()
	}

	s.updateSyncStatus("Fetching order history...", 0, 0)
	orders, err := s.client.GetAllOrders(func(count int) {
		s.updateSyncStatus(fmt.Sprintf("Found %d orders...", count), count, 0)
	})
	if err != nil {
		s.updateSyncStatus(fmt.Sprintf("Error: %v", err), 0, 0)
		return
	}

	total := len(orders)
	added := 0
	for i, o := range orders {
		s.mu.Lock()
		if !s.store.OrderExists(s.data, o.ID) {
			s.store.AddOrderFromAPI(s.data, o, s.data.BaselineDeliveryFee)
			added++
		}
		if (i+1)%50 == 0 {
			s.save()
		}
		s.mu.Unlock()
		s.updateSyncStatus(fmt.Sprintf("Processing order %d/%d...", i+1, total), i+1, total)
	}

	s.mu.Lock()
	s.save()
	s.mu.Unlock()

	// Always enrich after listing, so service fees + the restaurant map stay
	// populated. Only un-enriched orders are fetched, so this is a one-time
	// backfill then a couple of calls per new order on later syncs.
	log.Printf("Imported %d new orders; enriching...", added)
	s.runEnrichment()
}

func (s *Server) updateSyncStatus(status string, progress, total int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncStatus = status
	s.syncProgress = progress
	s.syncTotal = total
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	year := s.yearFromQuery(r)
	yearStats := stats.Calculate(s.ordersForYear(year), year, s.data.PlusMonthlyCost)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(yearStats)
}

// heatPoint is one weighted delivery location for the heatmap. Restaurant
// coordinates aren't in the order-history payload, so we map delivery locations;
// since these cluster on 1-2 addresses we aggregate by rounded coordinate and
// weight by order count.
type heatPoint struct {
	Lat    float64 `json:"lat"`
	Lng    float64 `json:"lng"`
	Weight int     `json:"weight"`
}

func (s *Server) handleOrderLocations(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	year := s.yearFromQuery(r)

	type acc struct {
		lat, lng float64
		count    int
	}
	agg := map[string]*acc{}
	for _, o := range s.ordersForYear(year) {
		if o.Status == "CANCELED" {
			continue
		}
		// Prefer real restaurant coords (populated by enrichment); fall back to
		// the delivery location until enriched.
		lat, lng := o.RestaurantLat, o.RestaurantLng
		if lat == 0 && lng == 0 {
			lat, lng = o.DeliveryLat, o.DeliveryLng
		}
		if lat == 0 && lng == 0 {
			continue
		}
		key := fmt.Sprintf("%.4f,%.4f", lat, lng)
		a := agg[key]
		if a == nil {
			a = &acc{lat: lat, lng: lng}
			agg[key] = a
		}
		a.count++
	}

	points := make([]heatPoint, 0, len(agg))
	for _, a := range agg {
		points = append(points, heatPoint{Lat: a.lat, Lng: a.lng, Weight: a.count})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(points)
}

var logoIDRe = regexp.MustCompile(`^[0-9]+$`)

// handleLogo serves a restaurant logo from disk, fetching + caching it from the
// CDN once on first request so the UI never pulls from the CDN directly.
func (s *Server) handleLogo(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("r")
	if !logoIDRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	cachePath := filepath.Join(s.logoDir, id)
	if b, err := os.ReadFile(cachePath); err == nil {
		w.Header().Set("Content-Type", http.DetectContentType(b))
		w.Header().Set("Cache-Control", "public, max-age=2592000")
		_, _ = w.Write(b)
		return
	}

	s.mu.RLock()
	url := s.data.RestaurantLogos[id]
	s.mu.RUnlock()
	if url == "" {
		http.NotFound(w, r)
		return
	}

	ct, data, err := s.client.FetchImage(url)
	if err != nil || len(data) == 0 {
		log.Printf("logo fetch %s failed: %v", id, err)
		http.NotFound(w, r)
		return
	}
	if err := os.WriteFile(cachePath, data, 0600); err != nil {
		log.Printf("logo cache write %s: %v", id, err)
	}
	if ct == "" {
		ct = http.DetectContentType(data)
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=2592000")
	_, _ = w.Write(data)
}

// save persists the data store, logging any error. Callers hold s.mu.
func (s *Server) save() {
	if err := s.store.SaveData(s.data); err != nil {
		log.Printf("save data: %v", err)
	}
}

func (s *Server) renderTemplate(w http.ResponseWriter, name string, data interface{}) {
	if err := s.templates.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("Template error: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func (s *Server) renderHTMXError(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "text/html")
	_, _ = fmt.Fprintf(w, `<div class="error-message">%s</div>`, template.HTMLEscapeString(message))
}
