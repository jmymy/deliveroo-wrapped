package stats

import (
	"sort"
	"time"

	"deliveroo-wrapped/internal/models"
)

// Calculate computes yearly statistics from orders. year == 0 means all years.
// plusMonthlyCost is the Deliveroo Plus price per month, used for ROI.
func Calculate(orders []models.StoredOrder, year int, plusMonthlyCost float64) *models.YearlyStats {
	st := &models.YearlyStats{
		Year:              year,
		OrdersByMonth:     make(map[int]int),
		SpendByMonth:      make(map[int]float64),
		OrdersByDayOfWeek: make(map[int]int),
		OrdersByHour:      make(map[int]int),
		OrdersByCuisine:   make(map[string]int),
		SpendByCuisine:    make(map[string]float64),
		OrdersByAddress:   make(map[string]int),
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

	for _, o := range orders {
		if year != 0 && o.PlacedAt.Year() != year {
			continue
		}
		if o.PlacedAt.IsZero() || o.Status == "CANCELED" {
			continue
		}

		st.TotalOrders++

		if st.Currency == "" && o.Currency != "" {
			st.Currency = o.Currency
		}

		// Money
		st.TotalSpent += o.Total
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

		// Patterns
		month := int(o.PlacedAt.Month())
		st.OrdersByMonth[month]++
		st.SpendByMonth[month] += o.Total
		st.OrdersByDayOfWeek[int(o.PlacedAt.Weekday())]++
		hour := o.PlacedAt.Hour()
		st.OrdersByHour[hour]++
		if o.PlacedAt.Weekday() == time.Saturday || o.PlacedAt.Weekday() == time.Sunday {
			st.WeekendOrders++
		} else {
			st.WeekdayOrders++
		}
		if hour >= 21 || hour < 4 {
			st.LateNightOrders++
		}
		months[o.PlacedAt.Format("2006-01")] = true
		ordersByDate[o.PlacedAt.Format("2006-01-02")]++

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
			e.TotalSpent += it.Price * float64(qty)

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
	}
	if st.TippedOrderCount > 0 {
		st.AvgTip = st.TotalTips / float64(st.TippedOrderCount)
	}
	if deliveryCount > 0 {
		st.AvgDeliveryMinutes = float64(totalDeliverySec) / float64(deliveryCount) / 60.0
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

	// Busiest month
	for m := 1; m <= 12; m++ {
		if st.OrdersByMonth[m] > st.OrdersByMonth[st.BusiestMonth] {
			st.BusiestMonth = m
		}
	}
	if st.BusiestMonth == 0 {
		st.BusiestMonth = 1
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
	st.DriverDataAvailable, st.UniqueDrivers, st.RepeatDriverOrders, st.TopDriver = CalculateDriverStats(orders, year)
	st.TopDishes = topDishes(dishAgg, 10)

	return st
}

// CalculateRestaurantStats aggregates orders per restaurant, returning the
// top-N leaderboard (by order count) and the unique-restaurant count.
func CalculateRestaurantStats(orders []models.StoredOrder, year int) ([]models.RestaurantLeaderboardEntry, int) {
	agg := make(map[string]*models.RestaurantLeaderboardEntry)
	for _, o := range orders {
		if year != 0 && o.PlacedAt.Year() != year {
			continue
		}
		if o.PlacedAt.IsZero() || o.Status == "CANCELED" || o.RestaurantName == "" {
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

// CalculateDriverStats detects repeat drivers (the "same driver again?" stat).
// Degrades gracefully: if no order carries a driver identity, available=false.
func CalculateDriverStats(orders []models.StoredOrder, year int) (available bool, unique, repeatOrders int, top *models.DriverLeaderboardEntry) {
	agg := make(map[string]*models.DriverLeaderboardEntry)
	seen := make(map[string]bool)
	for _, o := range orders {
		if year != 0 && o.PlacedAt.Year() != year {
			continue
		}
		if o.Status == "CANCELED" {
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

// GetStreakDays calculates the longest consecutive run of days with orders.
func GetStreakDays(orders []models.StoredOrder) (longestStreak, currentStreak int, streakStart time.Time) {
	if len(orders) == 0 {
		return 0, 0, time.Time{}
	}
	dates := make(map[string]bool)
	for _, o := range orders {
		if o.PlacedAt.IsZero() || o.Status == "CANCELED" {
			continue
		}
		dates[o.PlacedAt.Format("2006-01-02")] = true
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
		diff := sortedDates[i].Sub(sortedDates[i-1]).Hours() / 24
		if diff == 1 {
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
