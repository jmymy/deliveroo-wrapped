package stats

import (
	"math"
	"sort"
	"time"

	"deliveroo-wrapped/internal/models"
)

// haversineMi returns the great-circle distance in miles between two lat/lng
// points. Used for home→restaurant distance on enriched orders.
func haversineMi(lat1, lng1, lat2, lng2 float64) float64 {
	const earthMi = 3958.7613
	rad := math.Pi / 180
	dLat := (lat2 - lat1) * rad
	dLng := (lng2 - lng1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return earthMi * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// orderValueBands defines the histogram bands for the order-value distribution.
// Labels are currency-agnostic ranges; the template adds the currency context.
var orderValueBands = []struct {
	Label string
	Max   float64 // exclusive upper bound; the last band is the catch-all
}{
	{"<10", 10}, {"10–15", 15}, {"15–20", 20}, {"20–25", 25},
	{"25–30", 30}, {"30–40", 40}, {"40+", 1e18},
}

// orderValueBucket returns the index of the band an order total falls into.
func orderValueBucket(total float64) int {
	for i, b := range orderValueBands {
		if total < b.Max {
			return i
		}
	}
	return len(orderValueBands) - 1
}

// Calculate computes yearly statistics from orders. year == 0 means all years.
// plusMonthlyCost is the Deliveroo Plus price per month, used for ROI.
func Calculate(orders []models.StoredOrder, year int, plusMonthlyCost float64) *models.YearlyStats {
	st := &models.YearlyStats{
		Year:                year,
		OrdersByMonth:       make(map[int]int),
		SpendByMonth:        make(map[int]float64),
		DeliveryFeesByMonth: make(map[int]float64),
		ServiceFeesByMonth:  make(map[int]float64),
		OrdersByDayOfWeek:   make(map[int]int),
		OrdersByHour:        make(map[int]int),
		OrdersByCuisine:     make(map[string]int),
		SpendByCuisine:      make(map[string]float64),
		OrdersByAddress:     make(map[string]int),
	}
	for i := 1; i <= 12; i++ {
		st.OrdersByMonth[i] = 0
		st.SpendByMonth[i] = 0
	}
	for i := 0; i < 7; i++ {
		st.OrdersByDayOfWeek[i] = 0
	}
	for i := 0; i < 24; i++ {
		st.OrdersByHour[i] = 0
	}

	ordersByDate := make(map[string]int)
	months := make(map[string]bool) // distinct year-months, for Plus cost proxy
	dishAgg := make(map[string]*models.DishEntry)
	modAgg := make(map[string]*models.DishEntry)
	var totalDeliverySec int
	var deliveryCount int
	var etaCount, etaBeaten int
	var etaSumDiff, worstLate, earliest float64
	st.ShortestDeliveryMinutes = -1
	valueCounts := make([]int, len(orderValueBands))
	loc := models.OrderLocation()
	var firstDate, lastDate time.Time
	var distSum float64
	var distCount int

	for _, o := range orders {
		// Bucket by the order's local market time, not the stored UTC instant,
		// so near-midnight orders land on the right day / hour / year.
		pt := o.PlacedAt.In(loc)
		if year != 0 && pt.Year() != year {
			continue
		}
		if o.PlacedAt.IsZero() || !models.CountsTowardStats(o.Status) {
			continue
		}

		st.TotalOrders++
		if firstDate.IsZero() || pt.Before(firstDate) {
			firstDate = pt
		}
		if pt.After(lastDate) {
			lastDate = pt
		}

		if st.Currency == "" && o.Currency != "" {
			st.Currency = o.Currency
		}

		// Money
		st.TotalSpent += o.Total
		valueCounts[orderValueBucket(o.Total)]++
		st.TotalSubtotal += o.Subtotal
		st.TotalDeliveryFees += o.DeliveryFee
		st.TotalServiceFees += o.ServiceFee
		st.TotalOtherFees += o.SmallOrderFee + o.OtherFees
		st.TotalFees += o.TotalFees()
		st.TotalTips += o.Tip
		if o.Tip > 0 {
			st.TippedOrderCount++
		}

		// Plus savings
		st.PlusDeliverySaved += o.PlusDeliveryFeeSaved
		st.PlusServiceSaved += o.PlusServiceFeeSaved
		st.TotalPlusSavings += o.PlusSaved()

		// Patterns (bucketed in local market time, see pt above)
		month := int(pt.Month())
		st.OrdersByMonth[month]++
		st.SpendByMonth[month] += o.Total
		st.DeliveryFeesByMonth[month] += o.DeliveryFee
		st.ServiceFeesByMonth[month] += o.ServiceFee
		st.OrdersByDayOfWeek[int(pt.Weekday())]++
		hour := pt.Hour()
		st.OrdersByHour[hour]++
		if pt.Weekday() == time.Saturday || pt.Weekday() == time.Sunday {
			st.WeekendOrders++
		} else {
			st.WeekdayOrders++
		}
		if hour >= 21 || hour < 4 {
			st.LateNightOrders++
		}
		months[pt.Format("2006-01")] = true
		ordersByDate[pt.Format("2006-01-02")]++

		// Cuisine
		if o.Cuisine != "" {
			st.OrdersByCuisine[o.Cuisine]++
			st.SpendByCuisine[o.Cuisine] += o.Total
		}

		// Dishes + customisations
		for _, it := range o.Items {
			e, ok := dishAgg[it.Name]
			if !ok {
				e = &models.DishEntry{Name: it.Name}
				dishAgg[it.Name] = e
			}
			qty := it.Qty
			if qty == 0 {
				qty = 1
			}
			e.Count += qty
			e.TotalSpent += it.Price // Price is already the line total (unit × qty)

			for _, m := range it.Modifiers {
				me, ok := modAgg[m]
				if !ok {
					me = &models.DishEntry{Name: m}
					modAgg[m] = me
				}
				me.Count++
			}
		}

		// Where you order to
		if o.DeliveryAddressLabel != "" {
			st.OrdersByAddress[o.DeliveryAddressLabel]++
		}

		// Credits / refunds
		if o.CreditUsed > 0 {
			st.TotalCreditsUsed += o.CreditUsed
			st.CreditOrderCount++
		}

		// Delivery vs estimate (negative diff = early / beat the ETA)
		if !o.DeliveredAt.IsZero() && !o.EstimatedDeliveredAt.IsZero() {
			diff := o.DeliveredAt.Sub(o.EstimatedDeliveredAt).Minutes()
			etaCount++
			etaSumDiff += diff
			if diff <= 0 {
				etaBeaten++
			}
			if diff > worstLate {
				worstLate = diff
			}
			if diff < earliest {
				earliest = diff
			}
		}

		// Delivery time records
		if o.DeliveryDurationSec > 0 {
			mins := float64(o.DeliveryDurationSec) / 60.0
			totalDeliverySec += o.DeliveryDurationSec
			deliveryCount++
			if mins > st.LongestDeliveryMinutes {
				st.LongestDeliveryMinutes = mins
				st.LongestDeliveryDate = o.PlacedAt
				st.LongestDeliveryRestaurant = o.RestaurantName
			}
			if st.ShortestDeliveryMinutes < 0 || mins < st.ShortestDeliveryMinutes {
				st.ShortestDeliveryMinutes = mins
				st.ShortestDeliveryDate = o.PlacedAt
				st.ShortestDeliveryRestaurant = o.RestaurantName
			}
		}

		// Biggest order
		if o.Total > st.BiggestOrderTotal {
			st.BiggestOrderTotal = o.Total
			st.BiggestOrderRestaurant = o.RestaurantName
			st.BiggestOrderDate = o.PlacedAt
		}

		// Home → restaurant distance (enriched orders carry restaurant coords).
		if o.RestaurantLat != 0 && o.DeliveryLat != 0 {
			d := haversineMi(o.DeliveryLat, o.DeliveryLng, o.RestaurantLat, o.RestaurantLng)
			distSum += d
			distCount++
			if distCount == 1 || d > st.FarthestRestaurantMi { // set name on first sample too
				st.FarthestRestaurantMi = d
				st.FarthestRestaurantName = o.RestaurantName
			}
		}
	}

	if st.ShortestDeliveryMinutes < 0 {
		st.ShortestDeliveryMinutes = 0
	}
	if st.Currency == "" {
		st.Currency = "GBP" // fallback when no order carried a currency
	}

	// Derived money stats
	if st.TotalOrders > 0 {
		st.AvgOrderTotal = st.TotalSpent / float64(st.TotalOrders)
		st.TippedOrderPct = float64(st.TippedOrderCount) / float64(st.TotalOrders) * 100
		// Orders per week over the actual active span (first→last order), not a
		// hardcoded 52 — so partial years (e.g. the current one) aren't deflated.
		weeks := lastDate.Sub(firstDate).Hours() / 24 / 7
		if weeks < 1 {
			weeks = 1
		}
		st.AvgOrdersPerWeek = float64(st.TotalOrders) / weeks
	}
	if st.TippedOrderCount > 0 {
		st.AvgTip = st.TotalTips / float64(st.TippedOrderCount)
	}
	st.DeliverySampleCount = deliveryCount
	if deliveryCount > 0 {
		st.AvgDeliveryMinutes = float64(totalDeliverySec) / float64(deliveryCount) / 60.0
	}
	st.DistanceSampleCount = distCount
	if distCount > 0 {
		st.AvgRestaurantDistanceMi = distSum / float64(distCount)
	}

	// Plus ROI: subscription cost ~ monthly price * distinct active months.
	st.PlusSubscriptionCost = plusMonthlyCost * float64(len(months))
	if st.PlusSubscriptionCost > 0 {
		st.PlusROI = st.TotalPlusSavings / st.PlusSubscriptionCost
	}
	st.PlusNetBenefit = st.TotalPlusSavings - st.PlusSubscriptionCost

	// Fees expressed as N average meal-subtotals.
	avgSubtotal := 0.0
	if st.TotalOrders > 0 {
		avgSubtotal = st.TotalSubtotal / float64(st.TotalOrders)
	}
	if avgSubtotal > 0 {
		st.FeesAsMeals = st.TotalFees / avgSubtotal
	}

	// Most orders in one day
	for dateStr, count := range ordersByDate {
		if count > st.MostOrdersInOneDay {
			st.MostOrdersInOneDay = count
			st.MostOrdersDate = dateStr
		}
	}

	// Busiest month (stays 0 when there are no orders; the template guards on
	// TotalOrders and shows "—" rather than a misleading default of January).
	for m := 1; m <= 12; m++ {
		if st.OrdersByMonth[m] > st.OrdersByMonth[st.BusiestMonth] {
			st.BusiestMonth = m
		}
	}

	// Peak hour
	for h := 0; h < 24; h++ {
		if st.OrdersByHour[h] > st.OrdersByHour[st.PeakHour] {
			st.PeakHour = h
		}
	}

	// Delivery vs estimate
	st.EtaSampleCount = etaCount
	st.WorstLateMinutes = worstLate
	st.EarliestEtaMinute = earliest
	if etaCount > 0 {
		st.EtaBeatenPct = float64(etaBeaten) / float64(etaCount) * 100
		st.AvgVsEtaMinutes = etaSumDiff / float64(etaCount)
	}

	// Top customisations
	st.TopModifiers = topDishes(modAgg, 10)

	// Leaderboards
	st.TopRestaurants, st.UniqueRestaurants = CalculateRestaurantStats(orders, year)
	st.TopAddresses = CalculateAddressStats(orders, year)
	st.DriverDataAvailable, st.UniqueDrivers, st.RepeatDriverOrders, st.TopDriver = CalculateDriverStats(orders, year)
	st.TopDishes = topDishes(dishAgg, 10)

	// Order-value distribution
	for i, b := range orderValueBands {
		st.OrderValueBuckets = append(st.OrderValueBuckets, models.ValueBucket{Label: b.Label, Count: valueCounts[i]})
	}

	// Longest ordering streak (year-filtered to match the selected view).
	streakInput := orders
	if year != 0 {
		streakInput = make([]models.StoredOrder, 0, len(orders))
		for _, o := range orders {
			if o.PlacedAt.In(models.OrderLocation()).Year() == year {
				streakInput = append(streakInput, o)
			}
		}
	}
	st.LongestStreak, _, st.LongestStreakStart = GetStreakDays(streakInput)

	return st
}

// CalculateRestaurantStats aggregates orders per restaurant, returning the
// top-N leaderboard (by order count) and the unique-restaurant count.
func CalculateRestaurantStats(orders []models.StoredOrder, year int) ([]models.RestaurantLeaderboardEntry, int) {
	agg := make(map[string]*models.RestaurantLeaderboardEntry)
	for _, o := range orders {
		if year != 0 && o.PlacedAt.In(models.OrderLocation()).Year() != year {
			continue
		}
		if o.PlacedAt.IsZero() || !models.CountsTowardStats(o.Status) || o.RestaurantName == "" {
			continue
		}
		e, ok := agg[o.RestaurantName]
		if !ok {
			e = &models.RestaurantLeaderboardEntry{
				ID:         o.RestaurantID,
				Name:       o.RestaurantName,
				Cuisine:    o.Cuisine,
				FirstOrder: o.PlacedAt,
				LastOrder:  o.PlacedAt,
			}
			agg[o.RestaurantName] = e
		}
		if e.ID == "" {
			e.ID = o.RestaurantID
		}
		if e.Lat == 0 && o.RestaurantLat != 0 { // from an enriched order
			e.Lat = o.RestaurantLat
			e.Lng = o.RestaurantLng
		}
		e.OrderCount++
		e.TotalSpent += o.Total
		if o.PlacedAt.Before(e.FirstOrder) {
			e.FirstOrder = o.PlacedAt
		}
		if o.PlacedAt.After(e.LastOrder) {
			e.LastOrder = o.PlacedAt
		}
	}

	list := make([]models.RestaurantLeaderboardEntry, 0, len(agg))
	for _, e := range agg {
		list = append(list, *e)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].OrderCount != list[j].OrderCount {
			return list[i].OrderCount > list[j].OrderCount
		}
		return list[i].TotalSpent > list[j].TotalSpent
	})
	unique := len(list)
	if len(list) > 10 {
		list = list[:10]
	}
	return list, unique
}

// CalculateAddressStats aggregates orders by delivery-address label, with the
// centroid of each label's coordinates, sorted by count (top 5). Drives the
// dest-split bars and the delivery heatmap.
func CalculateAddressStats(orders []models.StoredOrder, year int) []models.AddressEntry {
	type acc struct {
		count          int
		latSum, lngSum float64
		coordN         int
	}
	agg := make(map[string]*acc)
	for _, o := range orders {
		if year != 0 && o.PlacedAt.In(models.OrderLocation()).Year() != year {
			continue
		}
		if o.PlacedAt.IsZero() || !models.CountsTowardStats(o.Status) || o.DeliveryAddressLabel == "" {
			continue
		}
		a := agg[o.DeliveryAddressLabel]
		if a == nil {
			a = &acc{}
			agg[o.DeliveryAddressLabel] = a
		}
		a.count++
		if o.DeliveryLat != 0 || o.DeliveryLng != 0 {
			a.latSum += o.DeliveryLat
			a.lngSum += o.DeliveryLng
			a.coordN++
		}
	}

	list := make([]models.AddressEntry, 0, len(agg))
	for name, a := range agg {
		e := models.AddressEntry{Name: name, Count: a.count}
		if a.coordN > 0 {
			e.Lat = a.latSum / float64(a.coordN)
			e.Lng = a.lngSum / float64(a.coordN)
		}
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Count > list[j].Count })
	// Percentage denominator is ALL labelled orders, computed before truncating to
	// the top 5 (otherwise "% of orders" overstates when there are >5 addresses).
	total := 0
	for _, e := range list {
		total += e.Count
	}
	if len(list) > 5 {
		list = list[:5]
	}
	if total > 0 {
		for i := range list {
			list[i].Pct = int(float64(list[i].Count)/float64(total)*100 + 0.5)
		}
	}
	return list
}

// CalculateDriverStats detects repeat drivers (the "same driver again?" stat).
// Degrades gracefully: if no order carries a driver identity, available=false.
func CalculateDriverStats(orders []models.StoredOrder, year int) (available bool, unique, repeatOrders int, top *models.DriverLeaderboardEntry) {
	agg := make(map[string]*models.DriverLeaderboardEntry)
	seen := make(map[string]bool)
	for _, o := range orders {
		if year != 0 && o.PlacedAt.In(models.OrderLocation()).Year() != year {
			continue
		}
		if !models.CountsTowardStats(o.Status) {
			continue
		}
		key := o.DriverKey()
		if key == "" {
			continue
		}
		available = true
		if seen[key] {
			repeatOrders++
		}
		seen[key] = true

		e, ok := agg[key]
		if !ok {
			e = &models.DriverLeaderboardEntry{
				Name:       o.DriverName,
				DriverID:   o.DriverID,
				FirstOrder: o.PlacedAt,
				LastOrder:  o.PlacedAt,
			}
			agg[key] = e
		}
		e.OrderCount++
		if o.PlacedAt.Before(e.FirstOrder) {
			e.FirstOrder = o.PlacedAt
		}
		if o.PlacedAt.After(e.LastOrder) {
			e.LastOrder = o.PlacedAt
		}
	}
	unique = len(agg)
	for _, e := range agg {
		if top == nil || e.OrderCount > top.OrderCount {
			top = e
		}
	}
	return available, unique, repeatOrders, top
}

func topDishes(agg map[string]*models.DishEntry, n int) []models.DishEntry {
	list := make([]models.DishEntry, 0, len(agg))
	for _, e := range agg {
		list = append(list, *e)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Count != list[j].Count {
			return list[i].Count > list[j].Count
		}
		return list[i].TotalSpent > list[j].TotalSpent
	})
	if len(list) > n {
		list = list[:n]
	}
	return list
}

// GetTopOrdersByDuration returns the N slowest deliveries.
func GetTopOrdersByDuration(orders []models.StoredOrder, n int) []models.StoredOrder {
	var valid []models.StoredOrder
	for _, o := range orders {
		if o.DeliveryDurationSec > 0 {
			valid = append(valid, o)
		}
	}
	sort.Slice(valid, func(i, j int) bool {
		return valid[i].DeliveryDurationSec > valid[j].DeliveryDurationSec
	})
	if n > len(valid) {
		n = len(valid)
	}
	return valid[:n]
}

// GetRecentOrders returns the N most recent orders.
func GetRecentOrders(orders []models.StoredOrder, n int) []models.StoredOrder {
	sorted := make([]models.StoredOrder, len(orders))
	copy(sorted, orders)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].PlacedAt.After(sorted[j].PlacedAt)
	})
	if n > len(sorted) {
		n = len(sorted)
	}
	return sorted[:n]
}

// CalculateDishInflation tracks how each dish's unit price has moved over the
// FULL order history (all-time — inflation is inherently multi-year, so it
// ignores the year filter). A dish qualifies only with enough repeat orders
// over a long-enough span. Unit price = line total / quantity. Sorted by the
// largest price increase first.
func CalculateDishInflation(orders []models.StoredOrder) []models.DishInflationEntry {
	const (
		minOrders   = 6
		minSpanDays = 180
		maxEntries  = 100 // safety cap; the UI list is scrollable
	)
	type occ struct {
		t     time.Time
		price float64
	}
	loc := models.OrderLocation()
	series := make(map[string][]occ)
	names := make(map[string][2]string) // key → {restaurant, dish}
	for _, o := range orders {
		if o.PlacedAt.IsZero() || !models.CountsTowardStats(o.Status) {
			continue
		}
		for _, it := range o.Items {
			if it.Name == "" || it.Price <= 0 {
				continue
			}
			qty := it.Qty
			if qty < 1 {
				qty = 1
			}
			key := o.RestaurantName + "\x00" + it.Name
			series[key] = append(series[key], occ{t: o.PlacedAt.In(loc), price: it.Price / float64(qty)})
			if _, ok := names[key]; !ok {
				names[key] = [2]string{o.RestaurantName, it.Name}
			}
		}
	}

	var out []models.DishInflationEntry
	for key, occs := range series {
		if len(occs) < minOrders {
			continue
		}
		sort.Slice(occs, func(i, j int) bool { return occs[i].t.Before(occs[j].t) })
		if occs[len(occs)-1].t.Sub(occs[0].t).Hours()/24 < minSpanDays {
			continue
		}
		// Monthly average unit price (smooths single-order noise + drives the line).
		sum := map[string]float64{}
		cnt := map[string]int{}
		var order []string
		for _, oc := range occs {
			m := oc.t.Format("2006-01")
			if cnt[m] == 0 {
				order = append(order, m)
			}
			sum[m] += oc.price
			cnt[m]++
		}
		points := make([]models.DishPricePoint, 0, len(order))
		for _, m := range order {
			points = append(points, models.DishPricePoint{Label: m, Price: sum[m] / float64(cnt[m])})
		}
		first := points[0].Price
		last := points[len(points)-1].Price
		if first <= 0 {
			continue
		}
		nm := names[key]
		out = append(out, models.DishInflationEntry{
			Name:       nm[1],
			Restaurant: nm[0],
			FirstPrice: first,
			LastPrice:  last,
			PctChange:  (last - first) / first * 100,
			OrderCount: len(occs),
			Points:     points,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PctChange != out[j].PctChange {
			return out[i].PctChange > out[j].PctChange
		}
		return out[i].OrderCount > out[j].OrderCount
	})
	if len(out) > maxEntries {
		out = out[:maxEntries]
	}
	return out
}

// GetStreakDays calculates the longest consecutive run of days with orders.
func GetStreakDays(orders []models.StoredOrder) (longestStreak, currentStreak int, streakStart time.Time) {
	if len(orders) == 0 {
		return 0, 0, time.Time{}
	}
	dates := make(map[string]bool)
	for _, o := range orders {
		if o.PlacedAt.IsZero() || !models.CountsTowardStats(o.Status) {
			continue
		}
		dates[o.PlacedAt.In(models.OrderLocation()).Format("2006-01-02")] = true
	}
	if len(dates) == 0 {
		return 0, 0, time.Time{}
	}
	var sortedDates []time.Time
	for d := range dates {
		t, _ := time.Parse("2006-01-02", d)
		sortedDates = append(sortedDates, t)
	}
	sort.Slice(sortedDates, func(i, j int) bool { return sortedDates[i].Before(sortedDates[j]) })

	longestStreak, currentStreak = 1, 1
	streakStart = sortedDates[0]
	longestStreakStart := sortedDates[0]
	for i := 1; i < len(sortedDates); i++ {
		// Tolerance instead of == 1: dates are parsed as UTC midnights so a day
		// is normally exactly 24h, but keep this robust to any DST/rounding edge.
		diff := sortedDates[i].Sub(sortedDates[i-1]).Hours() / 24
		if diff > 0.5 && diff < 1.5 {
			currentStreak++
			if currentStreak > longestStreak {
				longestStreak = currentStreak
				longestStreakStart = streakStart
			}
		} else {
			currentStreak = 1
			streakStart = sortedDates[i]
		}
	}
	return longestStreak, currentStreak, longestStreakStart
}

// MonthName returns the English name of a month number.
func MonthName(month int) string { return time.Month(month).String() }

// DayName returns the English name of a weekday (0 = Sunday).
func DayName(day int) string { return time.Weekday(day).String() }
