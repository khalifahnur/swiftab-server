package middleware

import (
	"context"
	"net/http"
	"swiftab/server/internal/util"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type contextKey string

const (
	AdminIDKey      contextKey = "adminId"
	RestaurantIDKey contextKey = "restaurantId"
	RoleKey         contextKey = "role"
)

func AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		cookie, err := r.Cookie("access_token")
		if err == nil && cookie.Value != "" {
			claims, err := util.ParseToken(cookie.Value)
			if err != nil {
				http.Error(w, "invalid or expired token", http.StatusUnauthorized)
				return
			}

			adminID, err := bson.ObjectIDFromHex(claims.AdminID)
			if err != nil {
				http.Error(w, "invalid admin ID in token", http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), AdminIDKey, adminID)
			ctx = context.WithValue(ctx, RoleKey, claims.Role)

			if claims.RestaurantID != "" {
				restaurantID, err := bson.ObjectIDFromHex(claims.RestaurantID)
				if err == nil {
					ctx = context.WithValue(ctx, RestaurantIDKey, restaurantID)
				}
			}

			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		restaurantHeader := r.Header.Get("X-Restaurant-ID")
		if restaurantHeader != "" {
			restaurantID, err := bson.ObjectIDFromHex(restaurantHeader)
			if err != nil {
				http.Error(w, "invalid restaurant ID format in header", http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), RestaurantIDKey, restaurantID)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		http.Error(w, "missing authentication", http.StatusUnauthorized)
	})
}
