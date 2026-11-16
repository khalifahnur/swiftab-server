package handler

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	models "swiftab/server/internal/model"
	"swiftab/server/internal/util"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// bucketStat is one day's (or month's) order activity.
type bucketStat struct {
	ID      string  `bson:"_id"`
	Orders  int     `bson:"orders"`
	Revenue float64 `bson:"revenue"`
}

type dailyReservation struct {
	ID           string `bson:"_id"`
	Reservations int    `bson:"reservations"`
	Guests       int    `bson:"guests"`
	Cancelled    int    `bson:"cancelled"`
}

type topItem struct {
	Name     string  `bson:"_id" json:"name"`
	Quantity int     `bson:"quantity" json:"quantity"`
	Revenue  float64 `bson:"revenue" json:"revenue"`
}

type paymentAgg struct {
	ID      string  `bson:"_id"`
	Count   int     `bson:"count"`
	Revenue float64 `bson:"revenue"`
}

type hourAgg struct {
	Hour   int `bson:"_id"`
	Orders int `bson:"orders"`
}

type staffAgg struct {
	ID      bson.ObjectID `bson:"_id"`
	Orders  int           `bson:"orders"`
	Revenue float64       `bson:"revenue"`
}

type openOrderAgg struct {
	Status string  `bson:"_id"`
	Count  int     `bson:"count"`
	Amount float64 `bson:"amount"`
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

// dashboardPeriod describes the reporting window the manager picked.
type dashboardPeriod struct {
	Name string
	// Start is the first instant of the window; PrevStart begins the window
	// of the same length just before it, used for "vs previous" figures.
	Start     time.Time
	PrevStart time.Time
	Monthly   bool // bucket by month rather than by day
	Buckets   []periodBucket
}

type periodBucket struct {
	Key   string // matches the aggregation's bucket id
	Label string // what the chart shows on its axis
}

// resolvePeriod turns ?period= into concrete dates in the business timezone.
// "weekly" is the last 7 days, "monthly" the last 30, "yearly" the last 12
// calendar months; anything else falls back to weekly.
func resolvePeriod(name string, today time.Time) dashboardPeriod {
	switch name {
	case "yearly":
		firstOfMonth := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, today.Location())
		start := firstOfMonth.AddDate(0, -11, 0)
		p := dashboardPeriod{Name: "yearly", Start: start, PrevStart: start.AddDate(-1, 0, 0), Monthly: true}
		for i := 0; i < 12; i++ {
			m := start.AddDate(0, i, 0)
			p.Buckets = append(p.Buckets, periodBucket{Key: m.Format("2006-01"), Label: m.Format("Jan")})
		}
		return p
	case "monthly":
		start := today.AddDate(0, 0, -29)
		p := dashboardPeriod{Name: "monthly", Start: start, PrevStart: start.AddDate(0, 0, -30)}
		for i := 0; i < 30; i++ {
			d := start.AddDate(0, 0, i)
			p.Buckets = append(p.Buckets, periodBucket{Key: d.Format("2006-01-02"), Label: d.Format("2 Jan")})
		}
		return p
	default:
		start := today.AddDate(0, 0, -6)
		p := dashboardPeriod{Name: "weekly", Start: start, PrevStart: start.AddDate(0, 0, -7)}
		for i := 0; i < 7; i++ {
			d := start.AddDate(0, 0, i)
			p.Buckets = append(p.Buckets, periodBucket{Key: d.Format("2006-01-02"), Label: d.Format("Mon")})
		}
		return p
	}
}

func hourLabel(hour int) string {
	switch {
	case hour == 0:
		return "12 AM"
	case hour < 12:
		return fmt.Sprintf("%d AM", hour)
	case hour == 12:
		return "12 PM"
	default:
		return fmt.Sprintf("%d PM", hour-12)
	}
}

// compare packages a figure with the previous period's and the change.
func compare(current, previous float64) map[string]interface{} {
	return map[string]interface{}{
		"value":         current,
		"previous":      previous,
		"changePercent": pctChange(current, previous),
	}
}

func (h *AdminHandler) GetDashboardStats(w http.ResponseWriter, r *http.Request) {
	restaurantID, err := h.restaurantID(r)
	if err != nil {
		util.JsonError(w, http.StatusBadRequest, "Valid Restaurant ID is required")
		return
	}

	ctx := r.Context()
	loc := h.location()
	tz := loc.String()
	now := time.Now().UTC()
	today := h.startOfDay(now)
	tomorrow := today.AddDate(0, 0, 1)
	period := resolvePeriod(r.URL.Query().Get("period"), today)

	// The KPI cards always compare today with yesterday over a 7-day
	// sparkline, whatever period the charts show.
	sparkStart := today.AddDate(0, 0, -6)
	dailyStart := sparkStart
	if !period.Monthly && period.Start.Before(dailyStart) {
		dailyStart = period.Start
	}

	orders := h.DB.Collection("orders")
	reservations := h.DB.Collection("reservations")

	dayOf := func(field string) bson.M {
		return bson.M{"$dateToString": bson.M{"format": "%Y-%m-%d", "date": field, "timezone": tz}}
	}
	monthOf := func(field string) bson.M {
		return bson.M{"$dateToString": bson.M{"format": "%Y-%m", "date": field, "timezone": tz}}
	}
	// Revenue only counts bills that were actually settled.
	paidRevenue := bson.M{"$sum": bson.M{"$cond": bson.A{
		bson.M{"$eq": bson.A{"$paymentStatus", "paid"}}, "$totalAmount", 0,
	}}}
	orderBuckets := func(from, to time.Time, bucket bson.M) bson.A {
		return bson.A{
			bson.M{"$match": bson.M{
				"restaurantId": restaurantID,
				"orderStatus":  bson.M{"$ne": "cancelled"},
				"createdAt":    bson.M{"$gte": from, "$lt": to},
			}},
			bson.M{"$group": bson.M{"_id": bucket, "orders": bson.M{"$sum": 1}, "revenue": paidRevenue}},
		}
	}

	var (
		dailyOrders   []bucketStat
		monthlyOrders []bucketStat
		prevOrders    []bucketStat
		dailyRes      []dailyReservation
		prevRes       []dailyReservation
		topItems      []topItem
		payments      []paymentAgg
		hours         []hourAgg
		staff         []staffAgg
		openOrders    []openOrderAgg
		upcoming      []models.Reservation
		recent        []models.Order
		layout        *models.RestaurantLayout
		tableStates   map[string]tableState
	)

	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	run := func(name string, fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("%s: %w", name, err)
				}
				mu.Unlock()
			}
		}()
	}
	aggregate := func(coll string, pipeline bson.A, out interface{}) error {
		cursor, err := h.DB.Collection(coll).Aggregate(ctx, pipeline)
		if err != nil {
			return err
		}
		return cursor.All(ctx, out)
	}

	run("daily orders", func() error {
		return aggregate("orders", orderBuckets(dailyStart, tomorrow, dayOf("$createdAt")), &dailyOrders)
	})
	if period.Monthly {
		run("monthly orders", func() error {
			return aggregate("orders", orderBuckets(period.Start, tomorrow, monthOf("$createdAt")), &monthlyOrders)
		})
	}
	run("previous orders", func() error {
		return aggregate("orders", orderBuckets(period.PrevStart, period.Start, bson.M{"$literal": "all"}), &prevOrders)
	})

	reservationBuckets := func(from, to time.Time, bucket interface{}) bson.A {
		isCancelled := bson.M{"$in": bson.A{"$status", bson.A{"cancelled", "no_show"}}}
		return bson.A{
			bson.M{"$match": bson.M{
				"restaurantId":               restaurantID,
				"reservationInfo.bookingFor": bson.M{"$gte": from, "$lt": to},
			}},
			bson.M{"$group": bson.M{
				"_id":          bucket,
				"reservations": bson.M{"$sum": bson.M{"$cond": bson.A{isCancelled, 0, 1}}},
				"guests":       bson.M{"$sum": bson.M{"$cond": bson.A{isCancelled, 0, "$reservationInfo.guests"}}},
				"cancelled":    bson.M{"$sum": bson.M{"$cond": bson.A{isCancelled, 1, 0}}},
			}},
		}
	}
	resStart := sparkStart
	if period.Start.Before(resStart) {
		resStart = period.Start
	}
	run("reservations", func() error {
		return aggregate("reservations", reservationBuckets(resStart, tomorrow, dayOf("$reservationInfo.bookingFor")), &dailyRes)
	})
	run("previous reservations", func() error {
		return aggregate("reservations", reservationBuckets(period.PrevStart, period.Start, "all"), &prevRes)
	})

	periodOrders := bson.M{
		"restaurantId": restaurantID,
		"orderStatus":  bson.M{"$ne": "cancelled"},
		"createdAt":    bson.M{"$gte": period.Start, "$lt": tomorrow},
	}

	run("top items", func() error {
		return aggregate("orders", bson.A{
			bson.M{"$match": periodOrders},
			bson.M{"$unwind": "$items"},
			bson.M{"$group": bson.M{
				"_id":      "$items.name",
				"quantity": bson.M{"$sum": "$items.quantity"},
				"revenue":  bson.M{"$sum": bson.M{"$multiply": bson.A{"$items.cost", "$items.quantity"}}},
			}},
			bson.M{"$sort": bson.D{{Key: "quantity", Value: -1}, {Key: "revenue", Value: -1}}},
			bson.M{"$limit": 5},
		}, &topItems)
	})

	run("payments", func() error {
		return aggregate("orders", bson.A{
			bson.M{"$match": bson.M{
				"restaurantId":  restaurantID,
				"paymentStatus": "paid",
				"createdAt":     bson.M{"$gte": period.Start, "$lt": tomorrow},
			}},
			bson.M{"$group": bson.M{"_id": "$paymentMethod", "count": bson.M{"$sum": 1}, "revenue": bson.M{"$sum": "$totalAmount"}}},
			bson.M{"$sort": bson.M{"revenue": -1}},
		}, &payments)
	})

	run("peak hours", func() error {
		return aggregate("orders", bson.A{
			bson.M{"$match": periodOrders},
			bson.M{"$group": bson.M{
				"_id":    bson.M{"$hour": bson.M{"date": "$createdAt", "timezone": tz}},
				"orders": bson.M{"$sum": 1},
			}},
		}, &hours)
	})

	run("staff", func() error {
		match := bson.M{}
		for k, v := range periodOrders {
			match[k] = v
		}
		match["servedBy"] = bson.M{"$type": "objectId"}
		return aggregate("orders", bson.A{
			bson.M{"$match": match},
			bson.M{"$group": bson.M{"_id": "$servedBy", "orders": bson.M{"$sum": 1}, "revenue": paidRevenue}},
			bson.M{"$sort": bson.M{"orders": -1}},
			bson.M{"$limit": 5},
		}, &staff)
	})

	run("open orders", func() error {
		return aggregate("orders", bson.A{
			bson.M{"$match": bson.M{
				"restaurantId": restaurantID,
				"orderStatus":  bson.M{"$in": bson.A{"placed", "served", "ready_to_pay"}},
				"createdAt":    bson.M{"$gte": now.Add(-openOrderWindow)},
			}},
			bson.M{"$group": bson.M{"_id": "$orderStatus", "count": bson.M{"$sum": 1}, "amount": bson.M{"$sum": "$totalAmount"}}},
		}, &openOrders)
	})

	run("upcoming reservations", func() error {
		opts := options.Find().SetSort(bson.M{"reservationInfo.bookingFor": 1}).SetLimit(6)
		cursor, err := reservations.Find(ctx, bson.M{
			"restaurantId":               restaurantID,
			"status":                     "active",
			"reservationInfo.bookingFor": bson.M{"$gte": now.Add(-15 * time.Minute), "$lt": tomorrow},
		}, opts)
		if err != nil {
			return err
		}
		return cursor.All(ctx, &upcoming)
	})

	run("recent orders", func() error {
		opts := options.Find().SetSort(bson.M{"createdAt": -1}).SetLimit(5)
		cursor, err := orders.Find(ctx, bson.M{"restaurantId": restaurantID}, opts)
		if err != nil {
			return err
		}
		return cursor.All(ctx, &recent)
	})

	run("layout", func() error {
		var err error
		layout, err = h.loadLayout(ctx, restaurantID)
		return err
	})
	run("table states", func() error {
		var err error
		tableStates, err = h.liveTableStates(r, restaurantID, now)
		return err
	})

	wg.Wait()
	if firstErr != nil {
		log.Printf("dashboard: %v", firstErr)
		util.JsonError(w, http.StatusInternalServerError, "Failed to fetch dashboard statistics")
		return
	}

	// --- Index the daily figures -------------------------------------------
	ordersByDay := map[string]bucketStat{}
	for _, b := range dailyOrders {
		ordersByDay[b.ID] = b
	}
	resByDay := map[string]dailyReservation{}
	for _, b := range dailyRes {
		resByDay[b.ID] = b
	}

	dayKey := func(offset int) string { return today.AddDate(0, 0, offset).Format("2006-01-02") }

	kpi := func(value func(day string) float64) map[string]interface{} {
		spark := make([]map[string]float64, 0, 7)
		for i := -6; i <= 0; i++ {
			spark = append(spark, map[string]float64{"value": value(dayKey(i))})
		}
		t, y := value(dayKey(0)), value(dayKey(-1))
		return map[string]interface{}{
			"today":         t,
			"yesterday":     y,
			"changePercent": pctChange(t, y),
			"sparkline":     spark,
		}
	}

	// --- Trend for the chosen period ---------------------------------------
	source := ordersByDay
	if period.Monthly {
		source = map[string]bucketStat{}
		for _, b := range monthlyOrders {
			source[b.ID] = b
		}
	}
	trend := make([]map[string]interface{}, 0, len(period.Buckets))
	var revenueTotal float64
	var ordersTotal int
	for _, b := range period.Buckets {
		stat := source[b.Key]
		revenueTotal += stat.Revenue
		ordersTotal += stat.Orders
		trend = append(trend, map[string]interface{}{
			"label":   b.Label,
			"date":    b.Key,
			"revenue": stat.Revenue,
			"orders":  stat.Orders,
		})
	}

	weeklyRevenue := make([]map[string]interface{}, 0, 7)
	for i := -6; i <= 0; i++ {
		d := today.AddDate(0, 0, i)
		stat := ordersByDay[d.Format("2006-01-02")]
		weeklyRevenue = append(weeklyRevenue, map[string]interface{}{
			"day":     d.Format("Mon"),
			"revenue": stat.Revenue,
			"orders":  stat.Orders,
		})
	}

	var reservationsTotal, cancelledTotal int
	for day, b := range resByDay {
		if day >= period.Start.Format("2006-01-02") {
			reservationsTotal += b.Reservations
			cancelledTotal += b.Cancelled
		}
	}

	var prevRevenue float64
	var prevOrderCount int
	if len(prevOrders) > 0 {
		prevRevenue, prevOrderCount = prevOrders[0].Revenue, prevOrders[0].Orders
	}
	var prevReservations, prevCancelled int
	if len(prevRes) > 0 {
		prevReservations, prevCancelled = prevRes[0].Reservations, prevRes[0].Cancelled
	}

	average := func(total float64, count int) float64 {
		if count == 0 {
			return 0
		}
		return math.Round(total / float64(count))
	}
	rate := func(part, whole int) float64 {
		if whole == 0 {
			return 0
		}
		return math.Round(float64(part)/float64(whole)*1000) / 10
	}

	summary := map[string]interface{}{
		"revenue":          compare(revenueTotal, prevRevenue),
		"orders":           compare(float64(ordersTotal), float64(prevOrderCount)),
		"avgOrderValue":    compare(average(revenueTotal, ordersTotal), average(prevRevenue, prevOrderCount)),
		"reservations":     compare(float64(reservationsTotal), float64(prevReservations)),
		"cancellationRate": compare(rate(cancelledTotal, reservationsTotal+cancelledTotal), rate(prevCancelled, prevReservations+prevCancelled)),
	}

	// --- Live floor ----------------------------------------------------------
	type areaBucket struct {
		Area                      string
		Occupied, Reserved, Total int
	}
	areas := make([]*areaBucket, 0, len(layout.Floors))
	areaByFloor := map[string]*areaBucket{}
	for _, f := range layout.Floors {
		a := &areaBucket{Area: f.Name}
		areas = append(areas, a)
		areaByFloor[f.ID] = a
	}

	var seated, reserved, activeGuests int
	for _, t := range layout.TablePosition {
		a := areaByFloor[t.FloorID]
		if a == nil {
			a = &areaBucket{Area: "Other"}
			areas = append(areas, a)
			areaByFloor[t.FloorID] = a
		}
		a.Total++
		switch tableStates[t.Name].Status {
		case "occupied":
			a.Occupied++
			seated++
			activeGuests += tableStates[t.Name].Guests
		case "reserved":
			a.Reserved++
			reserved++
		}
	}

	areaOccupancy := make([]map[string]interface{}, 0, len(areas))
	for _, a := range areas {
		percentage := 0
		if a.Total > 0 {
			percentage = int(math.Round(float64(a.Occupied) / float64(a.Total) * 100))
		}
		areaOccupancy = append(areaOccupancy, map[string]interface{}{
			"area":       a.Area,
			"occupied":   a.Occupied,
			"reserved":   a.Reserved,
			"total":      a.Total,
			"percentage": percentage,
		})
	}

	totalTables := len(layout.TablePosition)
	occupancyRate := 0
	if totalTables > 0 {
		occupancyRate = int(math.Round(float64(seated) / float64(totalTables) * 100))
	}

	var openCount, awaitingPayment int
	var unpaidAmount float64
	for _, o := range openOrders {
		openCount += o.Count
		unpaidAmount += o.Amount
		if o.Status == string(models.OrderReadyToPay) {
			awaitingPayment = o.Count
		}
	}

	todayRes := resByDay[dayKey(0)]
	live := map[string]interface{}{
		"reservationsToday": todayRes.Reservations,
		"guestsToday":       todayRes.Guests,
		"seatedTables":      seated,
		"reservedTables":    reserved,
		"totalTables":       totalTables,
		"totalSeats":        capacityOf(layout.TablePosition),
		"occupancyRate":     occupancyRate,
		"activeGuests":      activeGuests,
		"openOrders":        openCount,
		"awaitingPayment":   awaitingPayment,
		"unpaidAmount":      unpaidAmount,
	}

	// --- Payment split -------------------------------------------------------
	var paidTotal float64
	for _, p := range payments {
		paidTotal += p.Revenue
	}
	paymentMethods := make([]map[string]interface{}, 0, len(payments))
	for _, p := range payments {
		method := p.ID
		if method == "" {
			method = "unspecified"
		}
		percentage := 0
		if paidTotal > 0 {
			percentage = int(math.Round(p.Revenue / paidTotal * 100))
		}
		label := paymentLabel(method)
		if method == "unspecified" {
			label = "Not recorded"
		}
		paymentMethods = append(paymentMethods, map[string]interface{}{
			"method":     method,
			"label":      label,
			"count":      p.Count,
			"revenue":    p.Revenue,
			"percentage": percentage,
		})
	}

	// --- Peak hours ----------------------------------------------------------
	sort.Slice(hours, func(i, j int) bool { return hours[i].Hour < hours[j].Hour })
	peakHours := make([]map[string]interface{}, 0, len(hours))
	for _, hr := range hours {
		peakHours = append(peakHours, map[string]interface{}{
			"hour":   hr.Hour,
			"label":  hourLabel(hr.Hour),
			"orders": hr.Orders,
		})
	}

	// --- Staff ---------------------------------------------------------------
	staffRows := make([]map[string]interface{}, 0, len(staff))
	if len(staff) > 0 {
		ids := make(bson.A, 0, len(staff))
		for _, s := range staff {
			ids = append(ids, s.ID)
		}
		names := h.waiterNames(ctx, ids)
		for _, s := range staff {
			name, ok := names[s.ID]
			if !ok {
				name = "Former staff"
			}
			staffRows = append(staffRows, map[string]interface{}{
				"id":      s.ID.Hex(),
				"name":    name,
				"orders":  s.Orders,
				"revenue": s.Revenue,
			})
		}
	}

	upcomingRows := make([]map[string]interface{}, 0, len(upcoming))
	for _, res := range upcoming {
		upcomingRows = append(upcomingRows, map[string]interface{}{
			"_id":         res.ID.Hex(),
			"name":        res.ReservationInfo.Name,
			"time":        res.ReservationInfo.BookingFor,
			"guests":      res.ReservationInfo.Guests,
			"tableNumber": res.ReservationInfo.TableNumber,
			"diningArea":  res.ReservationInfo.DiningArea,
		})
	}

	if topItems == nil {
		topItems = []topItem{}
	}
	if recent == nil {
		recent = []models.Order{}
	}

	util.JsonResponse(w, http.StatusOK, map[string]interface{}{
		"period":   period.Name,
		"timezone": tz,
		"live":     live,
		"kpiTrends": map[string]interface{}{
			"revenue":      kpi(func(d string) float64 { return ordersByDay[d].Revenue }),
			"orders":       kpi(func(d string) float64 { return float64(ordersByDay[d].Orders) }),
			"reservations": kpi(func(d string) float64 { return float64(resByDay[d].Reservations) }),
			"guests":       kpi(func(d string) float64 { return float64(resByDay[d].Guests) }),
		},
		"summary":              summary,
		"trend":                trend,
		"weeklyRevenue":        weeklyRevenue,
		"areaOccupancy":        areaOccupancy,
		"topItems":             topItems,
		"paymentMethods":       paymentMethods,
		"peakHours":            peakHours,
		"staff":                staffRows,
		"upcomingReservations": upcomingRows,
		"recentOrders":         recent,
	})
}

// waiterNames resolves waiter ids to display names.
func (b *Base) waiterNames(ctx context.Context, ids bson.A) map[bson.ObjectID]string {
	names := map[bson.ObjectID]string{}
	opts := options.Find().SetProjection(bson.M{"firstname": 1, "lastname": 1})
	cursor, err := b.DB.Collection("waiters").Find(ctx, bson.M{"_id": bson.M{"$in": ids}}, opts)
	if err != nil {
		return names
	}
	var waiters []models.Waiter
	if err := cursor.All(ctx, &waiters); err != nil {
		return names
	}
	for _, w := range waiters {
		names[w.ID] = strings.TrimSpace(w.FirstName + " " + w.LastName)
	}
	return names
}
