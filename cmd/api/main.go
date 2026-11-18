package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"swiftab/server/internal/config"
	"swiftab/server/internal/db"
	"swiftab/server/internal/events"
	"swiftab/server/internal/handler"
	"swiftab/server/internal/middleware"
	"swiftab/server/internal/util"
)

const apiPrefix = "/api/v2"

func main() {
	log.Println("Starting Swiftab Engine ...")

	cfg := config.LoadConfig()

	mongoClient := db.Connect(cfg.MongoURI)
	swiftabDB := mongoClient.Database(cfg.DBName)
	db.EnsureIndexes(swiftabDB)

	imageService, err := util.NewCloudinaryService(cfg.CldName, cfg.CldApiKey, cfg.CldApiSk)
	if err != nil {
		// The API still runs; requests that upload an image fail with a clear
		// message instead of the whole service refusing to start.
		log.Printf("Image uploads are disabled: %v", err)
		imageService = nil
	}

	mailer := util.NewMailer(cfg.SMTP)
	if !mailer.Enabled() {
		log.Println("SMTP is not configured: waiter invite codes are shown in the dashboard only and password reset emails are unavailable")
	}
	if os.Getenv("JWT_SECRET") == "" && cfg.IsProd() {
		log.Println("WARNING: JWT_SECRET is not set; sessions are signed with an insecure default")
	}

	base := &handler.Base{
		DB:     swiftabDB,
		Cfg:    cfg,
		Image:  imageService,
		Mailer: mailer,
		Hub:    events.NewHub(),
	}

	auth := &middleware.Auth{DB: swiftabDB, RequireMobile: cfg.RequireMobileAuth}
	mux := newRouter(base, auth)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           logRequests(cors(cfg, mux)),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Printf("HTTP Server listening on port %s...", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP Listen error: %s\n", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server...")

	// Let in-flight requests finish; live-event streams are cut after the grace period.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		srv.Close()
	}
	mongoClient.Disconnect(context.Background())
}

// newRouter registers every API route. It is separate from main so the route
// table can be checked without a database.
func newRouter(base *handler.Base, auth *middleware.Auth) *http.ServeMux {
	admin := &handler.AdminHandler{Base: base}
	users := &handler.UserHandler{Base: base}
	menu := &handler.MenuHandler{Base: base}
	waiters := &handler.WaiterHandler{Base: base}
	tables := &handler.TableHandler{Base: base}
	reservations := &handler.ReservationHandler{Base: base}
	orders := &handler.OrderHandler{Base: base}
	promotions := &handler.PromotionHandler{Base: base}
	dine := &handler.DineHandler{Base: base, Orders: orders}

	mux := http.NewServeMux()

	// route registers a handler under the API prefix. pattern is
	// "METHOD /path".
	route := func(pattern string, h http.HandlerFunc, wrap ...func(http.Handler) http.Handler) {
		method, path, _ := strings.Cut(pattern, " ")
		var final http.Handler = h
		for i := len(wrap) - 1; i >= 0; i-- {
			final = wrap[i](final)
		}
		mux.Handle(method+" "+apiPrefix+path, final)
	}

	// --- Sign-in and sign-up (no session required) ---------------------------
	route("POST /admin/signup", admin.SignUpAdmin)
	route("POST /admin/login", admin.LoginAdmin)
	route("POST /admin/logout", admin.LogoutAdmin)

	route("POST /users/signup", users.SignUpUser)
	route("POST /users/login", users.LoginUser)
	route("POST /users/password/forgot", users.ForgotPassword)
	route("POST /users/password/verify", users.VerifyResetCode)
	route("POST /users/password/reset", users.ResetPassword)

	route("POST /waiters/verify", waiters.WaiterAuthentication)
	route("POST /waiters/password", waiters.WaiterPassword)
	route("POST /waiters/login", waiters.LoginWaiter)

	// --- Restaurant dashboard (admin session) --------------------------------
	route("GET /admin/info", admin.GetAdminInfo, auth.Admin)
	route("PUT /admin/profile", admin.UpdateProfile, auth.Admin)
	route("PUT /admin/password", admin.ChangePassword, auth.Admin)
	route("POST /admin/restaurant", admin.AddRestaurantData, auth.Admin)
	route("PUT /admin/restaurant", admin.UpdateRestaurant, auth.Admin)
	route("GET /admin/dashboard", admin.GetDashboardStats, auth.Admin)
	route("GET /admin/events", admin.StreamEvents, auth.Admin)

	route("POST /admin/waiters/signup", admin.WaiterSignUp, auth.Admin)
	route("GET /admin/waiters", admin.FetchWaiter, auth.Admin)
	route("POST /admin/waiters/{id}/invite", admin.ResendWaiterInvite, auth.Admin)
	route("DELETE /admin/waiters/{id}", admin.DeleteWaiter, auth.Admin)

	route("GET /admin/menu", menu.GetAdminMenu, auth.Admin)
	route("POST /admin/menu/categories", menu.AddCategory, auth.Admin)
	route("PUT /admin/menu/categories", menu.ReorderCategories, auth.Admin)
	route("PUT /admin/menu/categories/{key}", menu.RenameCategory, auth.Admin)
	route("DELETE /admin/menu/categories/{key}", menu.DeleteCategory, auth.Admin)
	route("POST /admin/menu/{menuType}", menu.AddMenu, auth.Admin)
	route("PUT /admin/menu/{menuType}/{itemId}", menu.UpdateMenuItem, auth.Admin)
	route("DELETE /admin/menu/{menuType}/{itemId}", menu.DeleteMenuItem, auth.Admin)

	route("GET /admin/layout/info", tables.FetchRestaurantInfo, auth.Admin)
	route("POST /admin/layout/info", tables.SaveLayoutInfo, auth.Admin)
	route("GET /admin/layout/tables", tables.FetchRestaurantTables, auth.Admin)
	route("POST /admin/layout/tables", tables.SaveTables, auth.Admin)
	route("GET /admin/layout/live", tables.FetchLiveFloor, auth.Admin)

	route("GET /admin/orders", orders.FetchAllOrder, auth.Admin)
	route("PUT /admin/orders/{orderId}", orders.AdminUpdateOrder, auth.Admin)
	route("GET /admin/reservations", reservations.FetchReservations, auth.Admin)
	route("PUT /admin/reservations/{id}/status", reservations.AdminUpdateReservation, auth.Admin)

	route("GET /admin/tables/qr", dine.TableCodes, auth.Admin)

	route("GET /admin/promotions", promotions.List, auth.Admin)
	route("POST /admin/promotions", promotions.Create, auth.Admin)
	route("PUT /admin/promotions/{id}", promotions.Update, auth.Admin)
	route("DELETE /admin/promotions/{id}", promotions.Delete, auth.Admin)

	// --- Read-only restaurant data ------------------------------------------
	route("GET /restaurants/menu", menu.GetMenu, auth.RestaurantScope)
	route("GET /restaurants/tables", tables.FetchResTable, auth.RestaurantScope)

	route("GET /restaurants/{restaurantId}", users.FetchRestaurant)
	route("GET /restaurants/{restaurantId}/menu", menu.GetMenuByRestaurantID)
	route("GET /restaurants/{restaurantId}/menu/sections", menu.GetMenuSections)
	route("GET /restaurants/{restaurantId}/tables", tables.FetchResTable)
	route("POST /reservations/availability", reservations.CheckAvailability)

	route("GET /users/restaurants", users.FetchAllRestaurants)
	route("GET /users/restaurants/nearby", users.FetchNearbyRestaurants)
	route("POST /users/restaurants/nearby", users.FetchNearbyRestaurants)
	route("GET /users/promotions", promotions.ListPublic)

	// --- Diner app -----------------------------------------------------------
	route("PUT /users/{userId}/profile", users.UpdateProfile, auth.Mobile)
	route("POST /users/{userId}/push-token", users.RegisterPushToken, auth.Mobile)
	route("DELETE /users/{userId}/push-token", users.RemovePushToken, auth.Mobile)
	route("GET /users/{userId}/recently-viewed", users.FetchRecentlyViewed, auth.Mobile)
	route("POST /users/{userId}/recently-viewed", users.AddRecentlyViewed, auth.Mobile)
	route("POST /users/{userId}/restaurants/{restaurantId}/reviews", users.AddReview, auth.Mobile)

	route("POST /users/{userId}/restaurants/{restaurantId}/reservations", reservations.CreateReservation, auth.Mobile)
	route("GET /users/{userId}/reservations/active", reservations.UserActiveReservation, auth.Mobile)
	route("GET /users/{userId}/reservations/completed", reservations.UserCompletedReservation, auth.Mobile)
	route("GET /users/{userId}/reservations/cancelled", reservations.UserCancelledReservation, auth.Mobile)
	route("POST /users/{userId}/reservations/cancel", reservations.UpdateStatusByUser, auth.Mobile)

	route("POST /users/orders", orders.CreateOrder, auth.Mobile)
	route("GET /users/{userId}/orders", orders.GetUserOrders, auth.Mobile)
	route("PUT /users/orders/{orderId}/complete", orders.UserCompleteOrder, auth.Mobile)
	route("PUT /users/orders/{orderId}/cancel", orders.UserCancelOrder, auth.Mobile)

	// Sharing a booking's menu so friends can add dishes to the host's bill.
	route("GET /users/{userId}/reservations/{reservationId}/tab", dine.UserTab, auth.Mobile)
	route("POST /users/{userId}/reservations/{reservationId}/tab", dine.UserShareTab, auth.Mobile)
	route("DELETE /users/{userId}/reservations/{reservationId}/tab", dine.UserStopTab, auth.Mobile)

	// --- Guests who scan a table's QR code ---------------------------------------
	// The code in the link is checked on every table request; after check-in
	// the guest token only reaches that one visit.
	route("GET /dine/tables/{restaurantId}/{tableId}", dine.Table)
	route("POST /dine/tables/{restaurantId}/{tableId}/checkin", dine.CheckIn)
	route("GET /dine/visit", dine.Visit)
	route("POST /dine/visit/orders", dine.PlaceOrder)
	route("PUT /dine/visit/orders/{orderId}/bill", dine.RequestBill)
	route("PUT /dine/visit/orders/{orderId}/cancel", dine.CancelOrder)
	route("POST /dine/visit/tab", dine.VisitShareTab)
	route("DELETE /dine/visit/tab", dine.VisitStopTab)

	// --- Friends who open a shared tab link ----------------------------------------
	// The link's code finds the booking; joining returns a guest token that
	// only adds dishes to that tab, and stops working when the host stops sharing.
	route("GET /tab/{code}", dine.TabPreview)
	route("POST /tab/{code}/join", dine.JoinTab)

	// --- Waiter app ----------------------------------------------------------
	route("GET /waiters/restaurants/{restaurantId}/orders", orders.GetWaiterOrders, auth.Mobile)
	route("GET /waiters/restaurants/{restaurantId}/floor", tables.FetchLiveFloor, auth.Mobile)
	route("PUT /waiters/orders/{orderId}/status", orders.UpdateOrderStatus, auth.Mobile)
	route("PUT /waiters/orders/{orderId}/complete", orders.WaiterCompleteOrder, auth.Mobile)
	route("GET /waiters/{waiterId}/summary", waiters.Summary, auth.Mobile)
	route("PUT /waiters/{waiterId}/profile", waiters.UpdateProfile, auth.Mobile)
	route("PUT /waiters/{waiterId}/duty", waiters.SetDuty, auth.Mobile)
	route("POST /waiters/{waiterId}/push-token", waiters.RegisterPushToken, auth.Mobile)
	route("DELETE /waiters/{waiterId}/push-token", waiters.RemovePushToken, auth.Mobile)

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
	})

	return mux
}

// cors lets the dashboard call the API from the browser with its session
// cookie. Because credentials are allowed, the origin must be one we trust:
// in production that is swiftab.co.ke plus anything listed in ALLOWED_ORIGIN.
func cors(cfg *config.Config, next http.Handler) http.Handler {
	allowed := map[string]bool{
		"https://www.swiftab.co.ke": true,
		"https://swiftab.co.ke":     true,
	}
	for _, o := range cfg.AllowedOrigins {
		allowed[o] = true
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		// Outside production any origin may call the API, which is what local
		// development against localhost needs.
		if origin != "" && (allowed[origin] || !cfg.IsProd()) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		w.Header().Add("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, X-Admin-ID, X-Restaurant-ID")
		w.Header().Set("Access-Control-Max-Age", "600")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// statusRecorder remembers the response status for the request log. It
// forwards Flush so streaming responses keep working.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
	})
}
