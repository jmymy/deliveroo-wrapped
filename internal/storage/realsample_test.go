package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"deliveroo-wrapped/internal/models"
)

// TestAddOrderFromAPI_RealSample exercises the adapter against a real captured
// order-history page (docs/api-samples/list-orders-reponse.json). The file is
// gitignored, so this skips when absent (e.g. in CI).
func TestAddOrderFromAPI_RealSample(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "api-samples", "list-orders-reponse.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("no captured sample at %s: %v", path, err)
	}

	var page models.OrderListResponse
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("unmarshal list page: %v", err)
	}
	if len(page.Orders) == 0 {
		t.Fatal("captured page has no orders")
	}

	s := &Storage{}
	store := &models.DataStore{}
	for _, o := range page.Orders {
		s.AddOrderFromAPI(store, o, 2.99)
	}

	if len(store.Orders) != len(page.Orders) {
		t.Fatalf("ingested %d, expected %d", len(store.Orders), len(page.Orders))
	}

	// Sanity: at least one delivered order should parse money + a timestamp.
	var okMoney, okTime, okItems int
	for _, o := range store.Orders {
		if o.Total > 0 {
			okMoney++
		}
		if !o.PlacedAt.IsZero() {
			okTime++
		}
		if len(o.Items) > 0 {
			okItems++
		}
	}
	if okMoney == 0 || okTime == 0 || okItems == 0 {
		t.Fatalf("parsing looks broken: money=%d time=%d items=%d (of %d)", okMoney, okTime, okItems, len(store.Orders))
	}
	t.Logf("ingested %d orders; %d with total>0, %d with placed time, %d with items",
		len(store.Orders), okMoney, okTime, okItems)
}
