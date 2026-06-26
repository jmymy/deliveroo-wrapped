package models

import (
	"os"
	"sync"
	"time"
)

var (
	orderLocOnce sync.Once
	orderLoc     *time.Location
)

// OrderLocation is the timezone used to bucket orders by local day / hour /
// year. Deliveroo timestamps are UTC, but the orders are placed in a local
// market (UK by default), so bucketing them in that market's timezone keeps
// near-midnight orders on the correct day and hour. Override with DELIVEROO_TZ
// for another market; falls back to UTC if the zone can't be loaded.
func OrderLocation() *time.Location {
	orderLocOnce.Do(func() {
		name := os.Getenv("DELIVEROO_TZ")
		if name == "" {
			name = "Europe/London"
		}
		loc, err := time.LoadLocation(name)
		if err != nil {
			loc = time.UTC
		}
		orderLoc = loc
	})
	return orderLoc
}

// CountsTowardStats reports whether an order with the given status should be
// included in aggregates. CANCELED and REJECTED orders never completed, so they
// must not inflate counts, spend, or pattern charts.
func CountsTowardStats(status string) bool {
	return status != "CANCELED" && status != "REJECTED"
}
