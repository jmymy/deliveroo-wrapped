package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"deliveroo-wrapped/internal/models"
)

// Storage handles persisting data to disk.
type Storage struct {
	dataDir string
}

// New creates a new storage instance rooted at dataDir.
func New(dataDir string) (*Storage, error) {
	// 0700: the data dir holds the auth token and delivery addresses/coords.
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("creating data directory: %w", err)
	}
	return &Storage{dataDir: dataDir}, nil
}

func (s *Storage) dataFilePath() string { return filepath.Join(s.dataDir, "deliveroo_data.json") }
func (s *Storage) authFilePath() string { return filepath.Join(s.dataDir, "auth.json") }

// LoadData loads the stored data from disk (empty store if none yet).
func (s *Storage) LoadData() (*models.DataStore, error) {
	data, err := os.ReadFile(s.dataFilePath())
	if os.IsNotExist(err) {
		return &models.DataStore{Orders: []models.StoredOrder{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading data file: %w", err)
	}
	var store models.DataStore
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, fmt.Errorf("parsing data file: %w", err)
	}
	if store.Orders == nil {
		store.Orders = []models.StoredOrder{}
	}
	return &store, nil
}

// SaveData persists the data store to disk, stamping LastSync.
func (s *Storage) SaveData(store *models.DataStore) error {
	store.LastSync = time.Now()
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling data: %w", err)
	}
	// 0600: contains delivery addresses/coordinates.
	if err := os.WriteFile(s.dataFilePath(), data, 0600); err != nil {
		return fmt.Errorf("writing data file: %w", err)
	}
	return nil
}

// LoadAuth loads the auth state from disk.
func (s *Storage) LoadAuth() (*models.AuthState, error) {
	data, err := os.ReadFile(s.authFilePath())
	if os.IsNotExist(err) {
		return &models.AuthState{Headers: map[string]string{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading auth file: %w", err)
	}
	var auth models.AuthState
	if err := json.Unmarshal(data, &auth); err != nil {
		return nil, fmt.Errorf("parsing auth file: %w", err)
	}
	if auth.Headers == nil {
		auth.Headers = map[string]string{}
	}
	return &auth, nil
}

// SaveAuth persists the auth state to disk.
func (s *Storage) SaveAuth(auth *models.AuthState) error {
	data, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling auth: %w", err)
	}
	// 0600: contains the bearer token + captured auth headers.
	if err := os.WriteFile(s.authFilePath(), data, 0600); err != nil {
		return fmt.Errorf("writing auth file: %w", err)
	}
	return nil
}

// OrderExists reports whether an order with the given ID is already stored.
func (s *Storage) OrderExists(store *models.DataStore, orderID string) bool {
	for _, o := range store.Orders {
		if o.ID == orderID {
			return true
		}
	}
	return false
}

// AddOrderFromAPI flattens an order-history list item into a StoredOrder and
// appends it. Money + timestamps arrive as strings (sometimes "") and are
// parsed here. Plus delivery savings are estimated: a delivered order showing a
// £0 delivery fee is assumed to have saved `baselineDeliveryFee`.
func (s *Storage) AddOrderFromAPI(store *models.DataStore, o models.APIOrder, baselineDeliveryFee float64) {
	placed := parseAPITime(o.SubmittedAt)
	delivered := parseAPITime(o.DeliveredAt)
	durationSec := 0
	if !placed.IsZero() && !delivered.IsZero() && delivered.After(placed) {
		durationSec = int(delivered.Sub(placed).Seconds())
	}

	deliveryFee := parseMoney(o.DeliveryFee)
	plusDeliverySaved := 0.0
	if o.Status == "DELIVERED" && deliveryFee == 0 {
		plusDeliverySaved = baselineDeliveryFee
	}

	items := make([]models.OrderItem, 0, len(o.Items))
	for _, it := range o.Items {
		price := parseMoney(it.TotalUnitPrice)
		if price == 0 {
			price = parseMoney(it.UnitPrice) * float64(maxInt(it.Quantity, 1))
		}
		var mods []string
		for _, m := range it.Modifiers {
			if m.Name != "" {
				mods = append(mods, m.Name)
			}
		}
		items = append(items, models.OrderItem{Name: it.Name, Qty: it.Quantity, Price: price, Modifiers: mods})
	}

	rLat, rLng := coord(o.Restaurant.Coordinates)
	dLat, dLng := coord(o.Address.Coordinates)

	var driverName, driverID string
	if len(o.Drivers) > 0 {
		driverName = o.Drivers[0].Name
		driverID = o.Drivers[0].ID
	}

	// Record the restaurant's logo URL once (fetched + cached lazily by the UI).
	if o.Restaurant.ID != "" && o.Restaurant.ImageURL != "" {
		if store.RestaurantLogos == nil {
			store.RestaurantLogos = map[string]string{}
		}
		if _, ok := store.RestaurantLogos[o.Restaurant.ID]; !ok {
			store.RestaurantLogos[o.Restaurant.ID] = sanitizeLogoURL(o.Restaurant.ImageURL)
		}
	}

	store.Orders = append(store.Orders, models.StoredOrder{
		ID:                   o.ID,
		RestaurantID:         o.Restaurant.ID,
		RestaurantName:       o.Restaurant.Name,
		Cuisine:              o.Restaurant.Category,
		PlacedAt:             placed,
		DeliveredAt:          delivered,
		EstimatedDeliveredAt: parseAPITime(o.EstimatedDeliveryAt),
		DeliveryDurationSec:  durationSec,
		Subtotal:             parseMoney(o.Subtotal),
		DeliveryFee:          deliveryFee,
		ServiceFee:           parseMoney(o.Fee),
		Tip:                  parseMoney(o.Tip),
		Total:                parseMoney(o.Total),
		CreditUsed:           parseMoney(o.CreditUsed),
		Currency:             o.CurrencyCode,
		DeliveryAddressLabel: o.Address.Label,
		PlusDeliveryFeeSaved: plusDeliverySaved,
		RestaurantLat:        rLat,
		RestaurantLng:        rLng,
		DeliveryLat:          dLat,
		DeliveryLng:          dLng,
		DriverName:           driverName,
		DriverID:             driverID,
		Items:                items,
		Status:               strings.ToUpper(strings.TrimSpace(o.Status)),
	})
}

// sanitizeLogoURL fills the {w}/{h} size placeholders in a roocdn image URL and
// drops the malformed {&quality} placeholder.
func sanitizeLogoURL(u string) string {
	return strings.NewReplacer("{w}", "96", "{h}", "96", "{&quality}", "").Replace(u)
}

// EnrichOrderFromDetail fills the service fee + real restaurant coordinates
// (which the list omits) onto a stored order, marking it enriched. Returns false
// if the order isn't found.
func (s *Storage) EnrichOrderFromDetail(store *models.DataStore, orderID string, d *models.OrderDetailResponse) bool {
	for i := range store.Orders {
		if store.Orders[i].ID != orderID {
			continue
		}
		store.Orders[i].ServiceFee = parseMoney(d.Fee)
		if len(d.Restaurant.Coordinates) >= 2 {
			// Detail restaurant coordinates are [lng, lat].
			store.Orders[i].RestaurantLng = d.Restaurant.Coordinates[0]
			store.Orders[i].RestaurantLat = d.Restaurant.Coordinates[1]
		}
		store.Orders[i].Enriched = true
		return true
	}
	return false
}

var moneyRe = regexp.MustCompile(`-?[0-9]+\.?[0-9]*`)

// parseMoney extracts a float from a money string like "16.49", "£0", or "".
func parseMoney(s string) float64 {
	m := moneyRe.FindString(strings.TrimSpace(s))
	if m == "" {
		return 0
	}
	f, _ := strconv.ParseFloat(m, 64)
	return f
}

// parseAPITime parses an RFC3339 timestamp (with or without fractional seconds);
// "" yields the zero time.
func parseAPITime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func coord(c []float64) (lat, lng float64) {
	if len(c) >= 2 {
		return c[0], c[1]
	}
	return 0, 0
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// GetOrdersForYear returns all orders placed in a specific year.
func (s *Storage) GetOrdersForYear(store *models.DataStore, year int) []models.StoredOrder {
	var orders []models.StoredOrder
	for _, o := range store.Orders {
		if o.PlacedAt.Year() == year {
			orders = append(orders, o)
		}
	}
	return orders
}

// GetAvailableYears returns the years that have order data, most recent first.
func (s *Storage) GetAvailableYears(store *models.DataStore) []int {
	yearMap := make(map[int]bool)
	for _, o := range store.Orders {
		y := o.PlacedAt.Year()
		if y > 2000 {
			yearMap[y] = true
		}
	}
	var years []int
	for y := range yearMap {
		years = append(years, y)
	}
	for i := 0; i < len(years)-1; i++ {
		for j := i + 1; j < len(years); j++ {
			if years[j] > years[i] {
				years[i], years[j] = years[j], years[i]
			}
		}
	}
	return years
}
