package models

import "time"

// =============================================================================
// Deliveroo API response types — matched to the real captured payloads
// (docs/api-samples/). The order-history LIST endpoint returns full per-order
// data (money, items, restaurant, delivery address, status, timestamps), so the
// MVP syncs from the list alone. Money + timestamps come back as STRINGS (and
// sometimes ""), so they're typed as string here and parsed in storage.
//
// Not in the list payload: service-fee breakdown, restaurant coordinates, and
// driver identity (the `drivers` array is empty even for delivered orders).
// Those would require per-order detail enrichment — a fast-follow.
// =============================================================================

// OrderListResponse is one page of GET /consumer/order-history/v1/orders.
type OrderListResponse struct {
	Orders []APIOrder `json:"orders"`
	Count  int        `json:"count"`
}

// APIOrder is a single order from the list endpoint.
type APIOrder struct {
	ID                  string        `json:"id"`
	OrderType           string        `json:"order_type"`
	Status              string        `json:"status"`
	Total               string        `json:"total"`
	Subtotal            string        `json:"subtotal"`
	Fee                 string        `json:"fee"`          // service fee (often "")
	DeliveryFee         string        `json:"delivery_fee"` // "0" when waived by Plus
	Tip                 string        `json:"tip"`
	CreditUsed          string        `json:"credit_used"`
	CurrencyCode        string        `json:"currency_code"`
	SubmittedAt         string        `json:"submitted_at"`
	EstimatedDeliveryAt string        `json:"estimated_delivery_at"`
	DeliveredAt         string        `json:"delivered_at"`
	Restaurant          APIRestaurant `json:"restaurant"`
	Address             APIAddress    `json:"address"`
	Items               []APIItem     `json:"items"`
	Drivers             []APIDriver   `json:"drivers"`
}

// APIRestaurant is the restaurant block on an order.
type APIRestaurant struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Category    string    `json:"category"`
	ImageURL    string    `json:"image_url"`   // has {w}/{h} placeholders
	Coordinates []float64 `json:"coordinates"` // [lat,lng]; [0,0] in the list payload
}

// APIAddress is the delivery address block on an order.
type APIAddress struct {
	Label       string    `json:"label"`
	City        string    `json:"city"`
	PostCode    string    `json:"post_code"`
	Coordinates []float64 `json:"coordinates"` // [lat,lng]
}

// APIItem is a single line item from the list payload.
type APIItem struct {
	Name           string        `json:"name"`
	Quantity       int           `json:"quantity"`
	UnitPrice      string        `json:"unit_price"`
	TotalUnitPrice string        `json:"total_unit_price"`
	Modifiers      []APIModifier `json:"modifiers"`
}

// APIModifier is a customisation on a line item, e.g. "Extra Spicy".
type APIModifier struct {
	Name string `json:"name"`
}

// APIDriver is the (usually empty) driver block; shape is best-effort since no
// captured order populated it.
type APIDriver struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// OrderDetailResponse is GET /orderapp/v1/users/{id}/orders/{orderId}. The
// service fee (`Fee`) and real restaurant coordinates live here, not in the
// list. (Several numeric IDs differ in type from the list payload, so this maps
// only the fields the list lacks.)
type OrderDetailResponse struct {
	Fee          string       `json:"fee"` // service fee
	DeliveryFee  string       `json:"delivery_fee"`
	FeeBreakdown []APIFeeLine `json:"fee_breakdown"`
	Restaurant   struct {
		Coordinates []float64 `json:"coordinates"` // [lng,lat] in the detail payload
	} `json:"restaurant"`
}

// APIFeeLine is one line of the human-facing fee breakdown, e.g.
// {"Service fee", "£0.99"} or {"Delivery fee", "Free"}.
type APIFeeLine struct {
	Title           string `json:"title"`
	FormattedAmount string `json:"formatted_amount"`
}

// APIUser is the account profile from GET /orderapp/v1/users/{id}.
type APIUser struct {
	// ID is numeric in the API payload (e.g. 64217723). It was previously typed
	// string, which made the whole GetUser decode fail (the numeric user ID
	// can't unmarshal into a string), so the profile silently never populated.
	// The user ID we actually use is derived from the auth credential, not here.
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	PreferredName string `json:"preferred_name"`
	Created       string `json:"created"` // e.g. "2022-04-01T14:13:10.657Z"
	Subscription  struct {
		Active           bool   `json:"active"`
		SubscriptionTier string `json:"subscription_tier"` // e.g. "DIAMOND"
		OfferUname       string `json:"offer_uname"`       // e.g. "uk_monthly_2499_2025Q2_no_trial"
	} `json:"subscription"`
}

// =============================================================================
// Stored types — our own flattened, query-friendly shape (NOT API-coupled).
// =============================================================================

// OrderItem is a stored line item.
type OrderItem struct {
	Name      string   `json:"name"`
	Qty       int      `json:"qty"`
	Price     float64  `json:"price"`
	Modifiers []string `json:"modifiers"`
}

// StoredOrder is one Deliveroo order, flattened for stats/querying.
type StoredOrder struct {
	ID                   string    `json:"id"`
	RestaurantID         string    `json:"restaurant_id"`
	RestaurantName       string    `json:"restaurant_name"`
	Cuisine              string    `json:"cuisine"`
	PlacedAt             time.Time `json:"placed_at"`
	DeliveredAt          time.Time `json:"delivered_at"`
	EstimatedDeliveredAt time.Time `json:"estimated_delivered_at"`
	// DeliveryDurationSec is DeliveredAt - PlacedAt, 0 if either is missing.
	DeliveryDurationSec int `json:"delivery_duration_sec"`

	Subtotal      float64 `json:"subtotal"`
	DeliveryFee   float64 `json:"delivery_fee"`
	ServiceFee    float64 `json:"service_fee"`
	SmallOrderFee float64 `json:"small_order_fee"`
	OtherFees     float64 `json:"other_fees"`
	Tip           float64 `json:"tip"`
	Total         float64 `json:"total"`
	CreditUsed    float64 `json:"credit_used"`
	Currency      string  `json:"currency"`

	DeliveryAddressLabel string `json:"delivery_address_label"` // e.g. "Home", "WeWork Monument"

	PlusDeliveryFeeSaved float64 `json:"plus_delivery_fee_saved"`
	PlusServiceFeeSaved  float64 `json:"plus_service_fee_saved"`

	RestaurantLat float64 `json:"restaurant_lat"`
	RestaurantLng float64 `json:"restaurant_lng"`
	DeliveryLat   float64 `json:"delivery_lat"`
	DeliveryLng   float64 `json:"delivery_lng"`

	DriverName string `json:"driver_name"`
	DriverID   string `json:"driver_id"`

	Items  []OrderItem `json:"items"`
	Status string      `json:"status"`
	// Enriched is true once service fee + restaurant coords have been filled
	// from the per-order detail endpoint.
	Enriched bool `json:"enriched"`
}

// TotalFees returns delivery + service + small-order + other fees.
func (o StoredOrder) TotalFees() float64 {
	return o.DeliveryFee + o.ServiceFee + o.SmallOrderFee + o.OtherFees
}

// PlusSaved returns total fees waived by Plus on this order.
func (o StoredOrder) PlusSaved() float64 {
	return o.PlusDeliveryFeeSaved + o.PlusServiceFeeSaved
}

// DriverKey returns a stable key for the driver, or "" if unidentifiable.
func (o StoredOrder) DriverKey() string {
	if o.DriverID != "" {
		return o.DriverID
	}
	return o.DriverName
}

// RestaurantLeaderboardEntry aggregates orders for one restaurant.
type RestaurantLeaderboardEntry struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Cuisine    string    `json:"cuisine"`
	OrderCount int       `json:"order_count"`
	TotalSpent float64   `json:"total_spent"`
	FirstOrder time.Time `json:"first_order"`
	LastOrder  time.Time `json:"last_order"`
	Lat        float64   `json:"lat"` // from enriched detail; 0 until enriched
	Lng        float64   `json:"lng"`
}

// DriverLeaderboardEntry aggregates orders for one driver.
type DriverLeaderboardEntry struct {
	Name       string    `json:"name"`
	DriverID   string    `json:"driver_id"`
	OrderCount int       `json:"order_count"`
	FirstOrder time.Time `json:"first_order"`
	LastOrder  time.Time `json:"last_order"`
}

// DishEntry aggregates a single dish across orders.
type DishEntry struct {
	Name       string  `json:"name"`
	Count      int     `json:"count"`
	TotalSpent float64 `json:"total_spent"`
}

// ValueBucket is one band of the order-value histogram (e.g. "£15–20" → 34).
type ValueBucket struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

// AddressEntry aggregates orders to one delivery address (drives the dest-split
// bars + the Leaflet heatmap). Lat/Lng is the centroid of that label's orders.
type AddressEntry struct {
	Name  string  `json:"name"`
	Count int     `json:"count"`
	Pct   int     `json:"pct"` // share of the address-set total
	Lat   float64 `json:"lat"`
	Lng   float64 `json:"lng"`
}

// DataStore holds all synced data, persisted as JSON.
type DataStore struct {
	LastSync    time.Time     `json:"last_sync"`
	Orders      []StoredOrder `json:"orders"`
	UserName    string        `json:"user_name"`
	PlusTier    string        `json:"plus_tier"`    // e.g. "DIAMOND" (from the user profile)
	MemberSince int           `json:"member_since"` // join year (from the profile's created date); 0 = unknown
	// PlusMonthlyCost is the Deliveroo Plus price per month, used for ROI.
	// Derived from the profile's offer_uname when available, else
	// DELIVEROO_PLUS_MONTHLY (default 3.49 GBP).
	PlusMonthlyCost float64 `json:"plus_monthly_cost"`
	// BaselineDeliveryFee is the assumed pre-Plus delivery fee, used to estimate
	// Plus delivery savings on £0-delivery orders. DELIVEROO_BASELINE_DELIVERY
	// (default 2.99 GBP).
	BaselineDeliveryFee float64 `json:"baseline_delivery_fee"`
	// RestaurantLogos maps restaurant ID → a sized roocdn image URL, populated at
	// ingest. Logos are fetched once and cached on disk; the UI loads them from
	// /api/logo?r=ID, never directly from the CDN.
	RestaurantLogos map[string]string `json:"restaurant_logos"`
}

// AuthState is the captured iOS-app credentials for token replay.
type AuthState struct {
	// Token is the full Authorization header value, e.g. "Bearer eyJ...".
	Token string `json:"token"`
	// Headers is the verbatim iOS-app header block (minus Authorization),
	// captured off the phone and replayed as-is to match the app fingerprint.
	Headers map[string]string `json:"headers"`
	// Host is the API base (scheme://host) derived from the capture, so non-UK
	// markets work; empty falls back to the client default.
	Host     string `json:"host"`
	UserName string `json:"user_name"`
	LoggedIn bool   `json:"logged_in"`
}

// YearlyStats holds computed statistics for a year (0 = all years).
type YearlyStats struct {
	Year     int    `json:"year"`
	Currency string `json:"currency"`

	// Counts
	TotalOrders       int     `json:"total_orders"`
	UniqueRestaurants int     `json:"unique_restaurants"`
	AvgOrdersPerWeek  float64 `json:"avg_orders_per_week"` // over the active span

	// Money
	TotalSpent        float64 `json:"total_spent"`
	TotalSubtotal     float64 `json:"total_subtotal"`
	TotalDeliveryFees float64 `json:"total_delivery_fees"`
	TotalServiceFees  float64 `json:"total_service_fees"`
	TotalOtherFees    float64 `json:"total_other_fees"`
	TotalFees         float64 `json:"total_fees"`
	TotalTips         float64 `json:"total_tips"`
	AvgOrderTotal     float64 `json:"avg_order_total"`
	AvgTip            float64 `json:"avg_tip"`
	TippedOrderCount  int     `json:"tipped_order_count"`
	TippedOrderPct    float64 `json:"tipped_order_pct"`

	// Plus membership
	TotalPlusSavings     float64 `json:"total_plus_savings"`
	PlusDeliverySaved    float64 `json:"plus_delivery_saved"`
	PlusServiceSaved     float64 `json:"plus_service_saved"`
	PlusSubscriptionCost float64 `json:"plus_subscription_cost"`
	PlusROI              float64 `json:"plus_roi"`
	PlusNetBenefit       float64 `json:"plus_net_benefit"`

	// Delivery time (minutes)
	DeliverySampleCount        int       `json:"delivery_sample_count"` // orders with a duration; 0 → hide
	AvgDeliveryMinutes         float64   `json:"avg_delivery_minutes"`
	LongestDeliveryMinutes     float64   `json:"longest_delivery_minutes"`
	LongestDeliveryDate        time.Time `json:"longest_delivery_date"`
	LongestDeliveryRestaurant  string    `json:"longest_delivery_restaurant"`
	ShortestDeliveryMinutes    float64   `json:"shortest_delivery_minutes"`
	ShortestDeliveryDate       time.Time `json:"shortest_delivery_date"`
	ShortestDeliveryRestaurant string    `json:"shortest_delivery_restaurant"`

	// Records
	MostOrdersInOneDay     int       `json:"most_orders_in_one_day"`
	MostOrdersDate         string    `json:"most_orders_date"`
	BiggestOrderTotal      float64   `json:"biggest_order_total"`
	BiggestOrderRestaurant string    `json:"biggest_order_restaurant"`
	BiggestOrderDate       time.Time `json:"biggest_order_date"`
	LongestStreak          int       `json:"longest_streak"` // consecutive days with an order
	LongestStreakStart     time.Time `json:"longest_streak_start"`

	// Order-value distribution (histogram bands by order total)
	OrderValueBuckets []ValueBucket `json:"order_value_buckets"`

	// Patterns
	OrdersByMonth       map[int]int     `json:"orders_by_month"`
	SpendByMonth        map[int]float64 `json:"spend_by_month"`
	DeliveryFeesByMonth map[int]float64 `json:"delivery_fees_by_month"`
	ServiceFeesByMonth  map[int]float64 `json:"service_fees_by_month"`
	OrdersByDayOfWeek   map[int]int     `json:"orders_by_day_of_week"`
	OrdersByHour        map[int]int     `json:"orders_by_hour"`
	WeekdayOrders       int             `json:"weekday_orders"`
	WeekendOrders       int             `json:"weekend_orders"`
	LateNightOrders     int             `json:"late_night_orders"`
	BusiestMonth        int             `json:"busiest_month"`
	PeakHour            int             `json:"peak_hour"` // hour 0-23 with the most orders

	// Delivery vs estimate ("Deliveroo beats its own ETA")
	EtaSampleCount    int     `json:"eta_sample_count"`
	EtaBeatenPct      float64 `json:"eta_beaten_pct"`     // % delivered on/before the ETA
	AvgVsEtaMinutes   float64 `json:"avg_vs_eta_minutes"` // negative = early
	WorstLateMinutes  float64 `json:"worst_late_minutes"`
	EarliestEtaMinute float64 `json:"earliest_eta_minute"` // most-early (negative)

	// Where you order to (delivery address label → count)
	OrdersByAddress map[string]int `json:"orders_by_address"`
	// TopAddresses is the address aggregate with centroid coords (dest-split + map).
	TopAddresses []AddressEntry `json:"top_addresses"`

	// Credits / refunds applied
	TotalCreditsUsed float64 `json:"total_credits_used"`
	CreditOrderCount int     `json:"credit_order_count"`

	// Top customisations (item modifiers)
	TopModifiers []DishEntry `json:"top_modifiers"`

	// Restaurant distance (home → restaurant, enriched orders only)
	AvgRestaurantDistanceMi float64 `json:"avg_restaurant_distance_mi"`
	FarthestRestaurantMi    float64 `json:"farthest_restaurant_mi"`
	FarthestRestaurantName  string  `json:"farthest_restaurant_name"`
	DistanceSampleCount     int     `json:"distance_sample_count"`

	// Leaderboards
	TopRestaurants []RestaurantLeaderboardEntry `json:"top_restaurants"`

	// Drivers
	DriverDataAvailable bool                    `json:"driver_data_available"`
	UniqueDrivers       int                     `json:"unique_drivers"`
	RepeatDriverOrders  int                     `json:"repeat_driver_orders"`
	TopDriver           *DriverLeaderboardEntry `json:"top_driver"`

	// Cuisine & dishes
	OrdersByCuisine map[string]int     `json:"orders_by_cuisine"`
	SpendByCuisine  map[string]float64 `json:"spend_by_cuisine"`
	TopDishes       []DishEntry        `json:"top_dishes"`

	// Fee economics comparison: total fees expressed as N average meal-subtotals.
	FeesAsMeals float64 `json:"fees_as_meals"`
}
