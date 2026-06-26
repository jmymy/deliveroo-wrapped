package stats

import (
	"testing"
	"time"

	"deliveroo-wrapped/internal/models"
)

func TestOrderValueBucket(t *testing.T) {
	cases := []struct {
		total float64
		want  int
	}{
		{0, 0}, {9.99, 0}, {10, 1}, {14.99, 1}, {15, 2},
		{24.99, 3}, {30, 5}, {39.99, 5}, {40, 6}, {250, 6},
	}
	for _, c := range cases {
		if got := orderValueBucket(c.total); got != c.want {
			t.Errorf("orderValueBucket(%.2f) = %d, want %d", c.total, got, c.want)
		}
	}
}

// TestCalculateExcludesCanceledAndRejected proves CANCELED and REJECTED orders
// don't inflate the count, and that the pattern buckets reconcile with the
// total (they're filled in the same loop, so any drift is a bug).
func TestCalculateExcludesCanceledAndRejected(t *testing.T) {
	base := time.Date(2026, 3, 10, 19, 0, 0, 0, time.UTC)
	mk := func(status string, dayOffset int) models.StoredOrder {
		return models.StoredOrder{
			Status:   status,
			Total:    18.50,
			PlacedAt: base.AddDate(0, 0, dayOffset),
		}
	}
	orders := []models.StoredOrder{
		mk("DELIVERED", 0), mk("DELIVERED", 1), mk("DELIVERED", 2),
		mk("CANCELED", 3), mk("REJECTED", 4),
	}
	st := Calculate(orders, 2026, 0)
	if st.TotalOrders != 3 {
		t.Fatalf("TotalOrders = %d, want 3 (CANCELED+REJECTED excluded)", st.TotalOrders)
	}
	dow, hour, month := 0, 0, 0
	for _, v := range st.OrdersByDayOfWeek {
		dow += v
	}
	for _, v := range st.OrdersByHour {
		hour += v
	}
	for _, v := range st.OrdersByMonth {
		month += v
	}
	if dow != 3 || hour != 3 || month != 3 {
		t.Errorf("pattern sums must reconcile with TotalOrders=3: dow=%d hour=%d month=%d", dow, hour, month)
	}
	// All three orders fall in the £15–20 band (index 2).
	if len(st.OrderValueBuckets) > 2 && st.OrderValueBuckets[2].Count != 3 {
		t.Errorf("expected 3 orders in the 15–20 band, got %d", st.OrderValueBuckets[2].Count)
	}
}
