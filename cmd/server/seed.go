package main

import (
	"fmt"
	"time"

	"deliveroo-wrapped/internal/models"
)

// seedOrders returns a synthetic, realistic order set for dev/demo so the
// dashboard renders without a live Deliveroo sync (DELIVEROO_SEED=1).
func seedOrders() []models.StoredOrder {
	type rest struct {
		name    string
		cuisine string
		lat,
		lng float64
	}
	restaurants := []rest{
		{"Franco Manca", "Pizza", 51.4613, -0.1156},
		{"Pho", "Vietnamese", 51.4625, -0.1140},
		{"Honest Burgers", "Burgers", 51.4639, -0.1101},
		{"Dishoom", "Indian", 51.4601, -0.1180},
		{"Wagamama", "Japanese", 51.4655, -0.1122},
		{"Franco Manca", "Pizza", 51.4613, -0.1156},
		{"The Begging Bowl", "Thai", 51.4708, -0.0750},
		{"Five Guys", "Burgers", 51.4639, -0.1101},
	}
	// Some orders share a driver to exercise the repeat-driver stat.
	drivers := []struct{ id, name string }{
		{"drv_001", "Marek"},
		{"drv_002", "Aisha"},
		{"drv_001", "Marek"},
		{"drv_003", "Tom"},
		{"drv_002", "Aisha"},
		{"", ""}, // some orders have no driver identity
	}
	// Two delivery destinations so the dest-split + heatmap have something to show.
	dests := []struct {
		label    string
		lat, lng float64
	}{
		{"Home", 51.4670, -0.1090},
		{"Office", 51.5155, -0.0890},
	}

	dishes := map[string][]struct {
		name  string
		price float64
	}{
		"Pizza":      {{"Margherita", 7.95}, {"Nduja", 9.50}, {"Garlic Bread", 4.00}},
		"Vietnamese": {{"Pho Bo", 11.50}, {"Summer Rolls", 6.00}},
		"Burgers":    {{"Cheeseburger", 10.50}, {"Rosemary Fries", 4.50}},
		"Indian":     {{"Chicken Ruby", 13.50}, {"Naan", 3.95}, {"Black Daal", 8.50}},
		"Japanese":   {{"Chicken Katsu", 12.95}, {"Edamame", 5.00}},
		"Thai":       {{"Green Curry", 12.00}, {"Pad Thai", 11.00}},
	}

	var orders []models.StoredOrder
	// Spread ~36 orders across 2025, weighted toward weekends/evenings.
	day := time.Date(2025, 1, 4, 19, 30, 0, 0, time.UTC)
	for i := 0; i < 36; i++ {
		rs := restaurants[i%len(restaurants)]
		dr := drivers[i%len(drivers)]
		placed := day.AddDate(0, 0, i*9).Add(time.Duration(i%5) * 17 * time.Minute)
		// Cluster two orders on one day mid-year to exercise "most orders in a day".
		if i == 20 {
			placed = time.Date(2025, 6, 14, 13, 0, 0, 0, time.UTC)
		}
		if i == 21 {
			placed = time.Date(2025, 6, 14, 20, 30, 0, 0, time.UTC)
		}

		menu := dishes[rs.cuisine]
		var items []models.OrderItem
		subtotal := 0.0
		for j, d := range menu {
			qty := 1
			if j == 0 && i%3 == 0 {
				qty = 2
			}
			items = append(items, models.OrderItem{Name: d.name, Qty: qty, Price: d.price})
			subtotal += d.price * float64(qty)
		}

		// Plus waives the delivery fee; service fee partly discounted.
		baselineDelivery := 2.49
		serviceFee := round2(subtotal * 0.06)
		tip := 0.0
		if i%2 == 0 {
			tip = float64(1 + i%4)
		}
		smallOrder := 0.0
		if subtotal < 12 {
			smallOrder = 1.50
		}
		total := round2(subtotal + serviceFee + smallOrder + tip) // delivery waived by Plus

		deliveryMin := 22 + (i*7)%40 // 22..61 min
		delivered := placed.Add(time.Duration(deliveryMin) * time.Minute)
		dest := dests[i%2] // alternate Home / Office (slightly weighted by the cluster days)

		orders = append(orders, models.StoredOrder{
			ID:                   fmt.Sprintf("seed_%02d", i),
			RestaurantName:       rs.name,
			Cuisine:              rs.cuisine,
			PlacedAt:             placed,
			DeliveredAt:          delivered,
			DeliveryDurationSec:  deliveryMin * 60,
			Subtotal:             round2(subtotal),
			DeliveryFee:          0, // waived by Plus
			ServiceFee:           serviceFee,
			SmallOrderFee:        smallOrder,
			Tip:                  tip,
			Total:                total,
			Currency:             "GBP",
			PlusDeliveryFeeSaved: baselineDelivery,
			PlusServiceFeeSaved:  round2(serviceFee * 0.5),
			RestaurantLat:        rs.lat,
			RestaurantLng:        rs.lng,
			DeliveryLat:          dest.lat,
			DeliveryLng:          dest.lng,
			DeliveryAddressLabel: dest.label,
			DriverName:           dr.name,
			DriverID:             dr.id,
			Items:                items,
			Status:               "DELIVERED",
		})
	}
	return orders
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}
