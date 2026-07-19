package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"swiftab/server/internal/config"
	"swiftab/server/internal/db"
	"swiftab/server/internal/handler"
	"swiftab/server/internal/middleware"
	"swiftab/server/internal/util"
)

func main() {
	log.Println("Starting ISP Engine ...")

	cfg := config.LoadConfig()
	port := cfg.Port
	mongoDB := cfg.MongoURI

	mongoClient := db.Connect(mongoDB)
	swiftabDB := mongoClient.Database("swiftab")

	imageService, err := util.NewCloudinaryService(cfg.CldName, cfg.CldApiKey, cfg.CldApiSk)
	if err != nil {
		log.Fatalf("Failed to initialize Cloudinary: %v", err)
	}

	adminHandler := &handler.AdminHandler{
		DB:        swiftabDB,
		Image:     imageService,
		SecretKey: cfg.SecretKey,
	}

	userHandler := &handler.UserHandler{DB: swiftabDB}
	menuHandler := &handler.MenuHandler{DB: swiftabDB, Image: imageService}
	waiterHandler := &handler.WaiterHandler{DB: swiftabDB}
	tableHandler := &handler.TableHandler{DB: swiftabDB}
	reserveHandler := &handler.ReservationHandler{DB: swiftabDB}
	orderHandler := &handler.OrderHandler{DB: swiftabDB}

	mux := http.NewServeMux()

	corsMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
			} else {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			}

			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Requested-With, X-Tenant-ID, X-Restaurant-ID")

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusOK)
				return
			}

			next.ServeHTTP(w, r)
		})
	}

	mux.HandleFunc("POST /api/v2/admin/signup", adminHandler.SignUpAdmin)
	mux.HandleFunc("POST /api/v2/admin/login", adminHandler.LoginAdmin)
	mux.HandleFunc("POST /api/v2/admin/logout", adminHandler.LogoutAdmin)

	mux.HandleFunc("POST /api/v2/users/signup", userHandler.SignUpUser)
	mux.HandleFunc("POST /api/v2/users/login", userHandler.LoginUser)

	mux.HandleFunc("POST /api/v2/waiters/verify", waiterHandler.WaiterAuthentication)
	mux.HandleFunc("POST /api/v2/waiters/password", waiterHandler.WaiterPassword)
	mux.HandleFunc("POST /api/v2/waiters/login", waiterHandler.LoginWaiter)

	mux.HandleFunc("GET /api/v2/restaurants/{restaurantId}/menu", menuHandler.GetMenu)
	mux.HandleFunc("GET /api/v2/restaurants/{restaurantId}/tables", tableHandler.FetchResTable)
	mux.HandleFunc("POST /api/v2/reservations/availability", reserveHandler.CheckAvailability)

	mux.Handle("GET /api/v2/admin/info", middleware.AuthMiddleware(http.HandlerFunc(adminHandler.GetAdminInfo)))
	mux.Handle("POST /api/v2/admin/restaurant", middleware.AuthMiddleware(http.HandlerFunc(adminHandler.AddRestaurantData)))
	mux.Handle("POST /api/v2/admin/waiters", middleware.AuthMiddleware(http.HandlerFunc(adminHandler.WaiterSignUp)))
	mux.Handle("GET /api/v2/admin/waiters", middleware.AuthMiddleware(http.HandlerFunc(adminHandler.FetchWaiter)))
	mux.Handle("DELETE /api/v2/admin/waiters/{id}", middleware.AuthMiddleware(http.HandlerFunc(adminHandler.DeleteWaiter)))

	mux.Handle("GET /api/v2/admin/dashboard", middleware.AuthMiddleware(http.HandlerFunc(adminHandler.GetDashboardStats)))
	mux.Handle("POST /api/v2/admin/menu/{menuType}", middleware.AuthMiddleware(http.HandlerFunc(menuHandler.AddMenu)))
	mux.Handle("PUT /api/v2/admin/menu/{menuType}/{itemId}", middleware.AuthMiddleware(http.HandlerFunc(menuHandler.UpdateMenuItem)))
	mux.Handle("DELETE /api/v2/admin/menu/{menuType}/{itemId}", middleware.AuthMiddleware(http.HandlerFunc(menuHandler.DeleteMenuItem)))
	mux.Handle("GET /api/v2/admin/layout/info", middleware.AuthMiddleware(http.HandlerFunc(tableHandler.FetchRestaurantInfo)))
	mux.Handle("POST /api/v2/admin/layout/info", middleware.AuthMiddleware(http.HandlerFunc(tableHandler.SaveLayoutInfo)))
	mux.Handle("GET /api/v2/admin/layout/tables", middleware.AuthMiddleware(http.HandlerFunc(tableHandler.FetchRestaurantTables)))
	mux.Handle("POST /api/v2/admin/layout/tables", middleware.AuthMiddleware(http.HandlerFunc(tableHandler.SaveTables)))
	mux.Handle("GET /api/v2/admin/orders", middleware.AuthMiddleware(http.HandlerFunc(orderHandler.FetchAllOrder)))
	mux.Handle("GET /api/v2/admin/reservations", middleware.AuthMiddleware(http.HandlerFunc(reserveHandler.FetchReservations)))
	mux.Handle("POST /api/v2/users/{userId}/restaurants/{restaurantId}/reservations", middleware.AuthMiddleware(http.HandlerFunc(reserveHandler.CreateReservation)))
	mux.Handle("GET /api/v2/users/{userId}/reservations/active", middleware.AuthMiddleware(http.HandlerFunc(reserveHandler.UserActiveReservation)))
	mux.Handle("GET /api/v2/users/{userId}/reservations/completed", middleware.AuthMiddleware(http.HandlerFunc(reserveHandler.UserCompletedReservation)))
	mux.Handle("POST /api/v2/users/orders", middleware.AuthMiddleware(http.HandlerFunc(orderHandler.CreateOrder)))
	mux.Handle("GET /api/v2/users/{userId}/orders", middleware.AuthMiddleware(http.HandlerFunc(orderHandler.GetUserOrders)))
	mux.Handle("PUT /api/v2/users/orders/{orderId}/complete", middleware.AuthMiddleware(http.HandlerFunc(orderHandler.UserCompleteOrder)))
	mux.Handle("GET /api/v2/waiters/restaurants/{restaurantId}/orders", middleware.AuthMiddleware(http.HandlerFunc(orderHandler.GetWaiterOrders)))
	mux.Handle("PUT /api/v2/waiters/orders/{orderId}/status", middleware.AuthMiddleware(http.HandlerFunc(orderHandler.UpdateOrderStatus)))
	mux.Handle("PUT /api/v2/waiters/orders/{orderId}/complete", middleware.AuthMiddleware(http.HandlerFunc(orderHandler.WaiterCompleteOrder)))

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
	})

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: corsMiddleware(mux),
	}

	go func() {
		log.Printf("HTTP Server listening on port %s...", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP Listen error: %s\n", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server...")
}
