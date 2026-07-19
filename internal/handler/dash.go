package handler

import (
	"math"
	"net/http"
	"sync"
	"time"

	models "swiftab/server/internal/model"
	"swiftab/server/internal/util"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type DailyRevenue struct {
	ID    string  `bson:"_id"`
	Value float64 `bson:"value"`
}

type DailyReservation struct {
	ID           string `bson:"_id"`
	Reservations int    `bson:"reservations"`
	Guests       int    `bson:"guests"`
}

type TopItem struct {
	ID       string  `bson:"_id"`
	Quantity int     `bson:"quantity"`
	Revenue  float64 `bson:"revenue"`
}

type PaymentAgg struct {
	ID      string  `bson:"_id"`
	Count   int     `bson:"count"`
	Revenue float64 `bson:"revenue"`
}

func pctChange(current, previous float64) int {
	if previous == 0 {
		if current > 0 {
			return 100
		}
		return 0
	}
	return int(math.Round(((current - previous) / previous) * 100))
}

func (h *AdminHandler) GetDashboardStats(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.getRestaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Valid Restaurant ID is required")
		return
	}

	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	sevenDaysAgo := today.Add(-6 * 24 * time.Hour)
	thirtyDaysAgo := today.Add(-29 * 24 * time.Hour)
	endOfToday := today.Add(24 * time.Hour).Add(-time.Nanosecond)

	todayKey := today.Format("2006-01-02")
	yesterdayKey := today.Add(-24 * time.Hour).Format("2006-01-02")

	// 2. Concurrency Setup for 7 simultaneous queries
	var wg sync.WaitGroup
	errChan := make(chan error, 7) // Buffered channel to catch errors without blocking

	// Data containers
	var revenueByDay []DailyRevenue
	var reservationsByDay []DailyReservation
	var topItemsAgg []TopItem
	var paymentAgg []PaymentAgg
	var layout models.RestaurantLayout
	var upcomingReservations []models.Reservation
	var recentOrders []models.Order

	wg.Add(7)

	// Query 1: Daily Revenue
	go func() {
		defer wg.Done()
		pipeline := bson.A{
			bson.M{"$match": bson.M{"restaurantId": restaurantID, "paymentStatus": "paid", "createdAt": bson.M{"$gte": sevenDaysAgo}}},
			bson.M{"$group": bson.M{"_id": bson.M{"$dateToString": bson.M{"format": "%Y-%m-%d", "date": "$createdAt"}}, "value": bson.M{"$sum": "$totalAmount"}}},
		}
		cursor, e := h.DB.Collection("orders").Aggregate(r.Context(), pipeline)
		if e == nil {
			e = cursor.All(r.Context(), &revenueByDay)
		}
		if e != nil {
			errChan <- e
		}
	}()

	// Query 2: Daily Reservations
	go func() {
		defer wg.Done()
		pipeline := bson.A{
			bson.M{"$match": bson.M{"restaurantId": restaurantID, "status": bson.M{"$ne": "cancelled"}, "reservationInfo.bookingFor": bson.M{"$gte": sevenDaysAgo}}},
			bson.M{"$group": bson.M{
				"_id":          bson.M{"$dateToString": bson.M{"format": "%Y-%m-%d", "date": "$reservationInfo.bookingFor"}},
				"reservations": bson.M{"$sum": 1},
				"guests":       bson.M{"$sum": "$reservationInfo.guests"},
			}},
		}
		cursor, e := h.DB.Collection("reservations").Aggregate(r.Context(), pipeline)
		if e == nil {
			e = cursor.All(r.Context(), &reservationsByDay)
		}
		if e != nil {
			errChan <- e
		}
	}()

	// Query 3: Top Items
	go func() {
		defer wg.Done()
		pipeline := bson.A{
			bson.M{"$match": bson.M{"restaurantId": restaurantID, "orderStatus": bson.M{"$ne": "cancelled"}, "createdAt": bson.M{"$gte": thirtyDaysAgo}}},
			bson.M{"$unwind": "$items"},
			bson.M{"$group": bson.M{
				"_id":      "$items.name",
				"quantity": bson.M{"$sum": "$items.quantity"},
				"revenue":  bson.M{"$sum": bson.M{"$multiply": bson.A{"$items.cost", "$items.quantity"}}},
			}},
			bson.M{"$sort": bson.M{"quantity": -1}},
			bson.M{"$limit": 5},
		}
		cursor, e := h.DB.Collection("orders").Aggregate(r.Context(), pipeline)
		if e == nil {
			e = cursor.All(r.Context(), &topItemsAgg)
		}
		if e != nil {
			errChan <- e
		}
	}()

	// Query 4: Payment Split
	go func() {
		defer wg.Done()
		pipeline := bson.A{
			bson.M{"$match": bson.M{"restaurantId": restaurantID, "paymentStatus": "paid", "createdAt": bson.M{"$gte": thirtyDaysAgo}}},
			bson.M{"$group": bson.M{"_id": "$paymentMethod", "count": bson.M{"$sum": 1}, "revenue": bson.M{"$sum": "$totalAmount"}}},
			bson.M{"$sort": bson.M{"count": -1}},
		}
		cursor, e := h.DB.Collection("orders").Aggregate(r.Context(), pipeline)
		if e == nil {
			e = cursor.All(r.Context(), &paymentAgg)
		}
		if e != nil {
			errChan <- e
		}
	}()

	// Query 5: Layout
	go func() {
		defer wg.Done()
		_ = h.DB.Collection("restaurantlayouts").FindOne(r.Context(), bson.M{"restaurantId": restaurantID}).Decode(&layout)
		// Ignored error explicitly since layout might not exist yet
	}()

	// Query 6: Upcoming Reservations Today
	go func() {
		defer wg.Done()
		filter := bson.M{
			"restaurantId":               restaurantID,
			"status":                     "active",
			"reservationInfo.bookingFor": bson.M{"$gte": now, "$lte": endOfToday},
		}
		opts := options.Find().SetSort(bson.M{"reservationInfo.bookingFor": 1}).SetLimit(5)
		cursor, e := h.DB.Collection("reservations").Find(r.Context(), filter, opts)
		if e == nil {
			e = cursor.All(r.Context(), &upcomingReservations)
		}
		if e != nil {
			errChan <- e
		}
	}()

	// Query 7: Recent Orders
	go func() {
		defer wg.Done()
		opts := options.Find().SetSort(bson.M{"createdAt": -1}).SetLimit(5)
		cursor, e := h.DB.Collection("orders").Find(r.Context(), bson.M{"restaurantId": restaurantID}, opts)
		if e == nil {
			e = cursor.All(r.Context(), &recentOrders)
		}
		if e != nil {
			errChan <- e
		}
	}()

	// Wait for all queries to finish
	wg.Wait()
	close(errChan)

	// Check if any query failed fatally
	if len(errChan) > 0 {
		util.JsonError(w, http.StatusInternalServerError, "Failed to fetch dashboard statistics")
		return
	}

	// 3. Data Processing & Formatting (O(1) lookups)
	revMap := make(map[string]float64)
	for _, r := range revenueByDay {
		revMap[r.ID] = r.Value
	}

	resMap := make(map[string]int)
	guestsMap := make(map[string]int)
	for _, r := range reservationsByDay {
		resMap[r.ID] = r.Reservations
		guestsMap[r.ID] = r.Guests
	}

	// KPI Builder Helper
	buildKpi := func(m map[string]float64) map[string]interface{} {
		t := m[todayKey]
		y := m[yesterdayKey]
		spark := make([]map[string]float64, 7)
		for i := 0; i < 7; i++ {
			d := today.Add(time.Duration(i-6) * 24 * time.Hour).Format("2006-01-02")
			spark[i] = map[string]float64{"value": m[d]}
		}
		return map[string]interface{}{
			"today":         t,
			"yesterday":     y,
			"changePercent": pctChange(t, y),
			"sparkline":     spark,
		}
	}

	// Int variation of KPI Builder
	buildKpiInt := func(m map[string]int) map[string]interface{} {
		t := m[todayKey]
		y := m[yesterdayKey]
		spark := make([]map[string]int, 7)
		for i := 0; i < 7; i++ {
			d := today.Add(time.Duration(i-6) * 24 * time.Hour).Format("2006-01-02")
			spark[i] = map[string]int{"value": m[d]}
		}
		return map[string]interface{}{
			"today":         t,
			"yesterday":     y,
			"changePercent": pctChange(float64(t), float64(y)),
			"sparkline":     spark,
		}
	}

	// Format Weekly Revenue
	var formattedWeeklyRevenue []map[string]interface{}
	for i := 0; i < 7; i++ {
		d := sevenDaysAgo.Add(time.Duration(i) * 24 * time.Hour)
		dayStr := d.Format("2006-01-02")
		formattedWeeklyRevenue = append(formattedWeeklyRevenue, map[string]interface{}{
			"day":     d.Weekday().String()[:3], // "Sun", "Mon", etc.
			"revenue": revMap[dayStr],
		})
	}

	// Area Occupancy Logic
	type AreaBucket struct {
		Area     string
		Occupied int
		Total    int
	}
	buckets := make(map[string]*AreaBucket)
	for _, area := range layout.DiningAreas {
		buckets[area] = &AreaBucket{Area: area, Occupied: 0, Total: 0}
	}

	for _, table := range layout.TablePosition {
		key := table.FloorID
		if key == "" {
			key = "Other"
		}
		if _, exists := buckets[key]; !exists {
			buckets[key] = &AreaBucket{Area: key, Occupied: 0, Total: 0}
		}
		buckets[key].Total++
		if table.Status == "occupied" { // Match your Node.js status string
			buckets[key].Occupied++
		}
	}

	var areaOccupancy []map[string]interface{}
	for _, b := range buckets {
		percentage := 0
		if b.Total > 0 {
			percentage = int(math.Round((float64(b.Occupied) / float64(b.Total)) * 100))
		}
		areaOccupancy = append(areaOccupancy, map[string]interface{}{
			"area":       b.Area,
			"occupied":   b.Occupied,
			"total":      b.Total,
			"percentage": percentage,
		})
	}

	// Payment Methods
	totalPayments := 0
	for _, p := range paymentAgg {
		totalPayments += p.Count
	}

	var formattedPaymentMethods []map[string]interface{}
	for _, p := range paymentAgg {
		if p.ID == "" { // Ignore empty IDs like in Node
			continue
		}
		percentage := 0
		if totalPayments > 0 {
			percentage = int(math.Round((float64(p.Count) / float64(totalPayments)) * 100))
		}
		formattedPaymentMethods = append(formattedPaymentMethods, map[string]interface{}{
			"method":     p.ID,
			"count":      p.Count,
			"revenue":    p.Revenue,
			"percentage": percentage,
		})
	}

	// Format Upcoming Reservations
	var formattedReservations []map[string]interface{}
	for _, r := range upcomingReservations {
		formattedReservations = append(formattedReservations, map[string]interface{}{
			"_id":         r.ID.Hex(),
			"name":        r.ReservationInfo.Name,
			"time":        r.ReservationInfo.BookingFor,
			"guests":      r.ReservationInfo.Guests,
			"tableNumber": r.ReservationInfo.TableNumber,
			"diningArea":  r.ReservationInfo.DiningArea,
		})
	}

	// Ensure no nulls in final JSON
	if topItemsAgg == nil {
		topItemsAgg = []TopItem{}
	}
	if areaOccupancy == nil {
		areaOccupancy = []map[string]interface{}{}
	}
	if formattedPaymentMethods == nil {
		formattedPaymentMethods = []map[string]interface{}{}
	}
	if formattedReservations == nil {
		formattedReservations = []map[string]interface{}{}
	}
	if recentOrders == nil {
		recentOrders = []models.Order{}
	}

	// 4. Final JSON Construction
	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"kpiTrends": map[string]interface{}{
			"revenue":      buildKpi(revMap),
			"reservations": buildKpiInt(resMap),
			"guests":       buildKpiInt(guestsMap),
		},
		"weeklyRevenue":        formattedWeeklyRevenue,
		"areaOccupancy":        areaOccupancy,
		"topItems":             topItemsAgg,
		"paymentMethods":       formattedPaymentMethods,
		"upcomingReservations": formattedReservations,
		"recentOrders":         recentOrders,
	})
}
