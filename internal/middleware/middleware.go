package middleware

import (
	"context"
	"net/http"
	"strings"

	"swiftab/server/internal/util"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type contextKey string

const (
	AdminIDKey      contextKey = "adminId"
	RestaurantIDKey contextKey = "restaurantId"
	RoleKey         contextKey = "role"
	UserIDKey       contextKey = "userId"
	WaiterIDKey     contextKey = "waiterId"
)

const AccessCookie = "access_token"

// Auth builds the authentication middlewares. It needs the database so an
// admin whose token predates their restaurant setup still resolves a
// restaurant without signing in again.
type Auth struct {
	DB *mongo.Database
	// RequireMobile rejects user/waiter requests that carry no valid bearer token.
	RequireMobile bool
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// adminClaims extracts a valid admin token from the session cookie or, for
// API clients, the Authorization header.
func adminClaims(r *http.Request) (*util.Claims, bool) {
	token := ""
	if cookie, err := r.Cookie(AccessCookie); err == nil {
		token = cookie.Value
	}
	if token == "" {
		token = bearerToken(r)
	}
	if token == "" {
		return nil, false
	}
	claims, err := util.ParseToken(token)
	if err != nil || claims.AdminID == "" {
		return nil, false
	}
	// Tokens issued before roles were recorded have an empty role.
	if claims.Role != "" && claims.Role != util.RoleAdmin {
		return nil, false
	}
	return claims, true
}

func (a *Auth) adminContext(r *http.Request, claims *util.Claims) (context.Context, bool) {
	adminID, err := bson.ObjectIDFromHex(claims.AdminID)
	if err != nil {
		return nil, false
	}

	ctx := context.WithValue(r.Context(), AdminIDKey, adminID)
	ctx = context.WithValue(ctx, RoleKey, util.RoleAdmin)

	if claims.RestaurantID != "" {
		if restaurantID, err := bson.ObjectIDFromHex(claims.RestaurantID); err == nil {
			return context.WithValue(ctx, RestaurantIDKey, restaurantID), true
		}
	}

	// The token carries no restaurant: the admin may have completed setup
	// after signing in, so check the account itself.
	if a.DB != nil {
		var admin struct {
			RestaurantID *bson.ObjectID `bson:"restaurantId"`
		}
		opts := options.FindOne().SetProjection(bson.M{"restaurantId": 1})
		err := a.DB.Collection("admins").FindOne(r.Context(), bson.M{"_id": adminID}, opts).Decode(&admin)
		if err == nil && admin.RestaurantID != nil {
			ctx = context.WithValue(ctx, RestaurantIDKey, *admin.RestaurantID)
		}
	}
	return ctx, true
}

// Admin requires a signed-in restaurant admin. Every /admin route uses it.
func (a *Auth) Admin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := adminClaims(r)
		if !ok {
			util.JsonError(w, http.StatusUnauthorized, "Please sign in to continue")
			return
		}
		ctx, ok := a.adminContext(r, claims)
		if !ok {
			util.JsonError(w, http.StatusUnauthorized, "Invalid session")
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RestaurantScope serves read-only restaurant data to either a signed-in admin
// or a client that names the restaurant in X-Restaurant-ID. It must never
// guard a route that changes data: the header is not a credential.
func (a *Auth) RestaurantScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if claims, ok := adminClaims(r); ok {
			if ctx, ok := a.adminContext(r, claims); ok {
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}

		if header := r.Header.Get("X-Restaurant-ID"); header != "" {
			restaurantID, err := bson.ObjectIDFromHex(header)
			if err != nil {
				util.JsonError(w, http.StatusBadRequest, "Invalid restaurant ID format in header")
				return
			}
			ctx := context.WithValue(r.Context(), RestaurantIDKey, restaurantID)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		util.JsonError(w, http.StatusUnauthorized, "Missing authentication")
	})
}

// Mobile authenticates the diner and waiter apps. A valid bearer token puts
// the caller's identity in the context so handlers can refuse to act on
// someone else's data. Requests without a token are let through unless
// RequireMobile is set, which keeps already-released app builds working.
func (a *Auth) Mobile(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			if a.RequireMobile {
				util.JsonError(w, http.StatusUnauthorized, "Please sign in to continue")
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		claims, err := util.ParseToken(token)
		if err != nil {
			util.JsonError(w, http.StatusUnauthorized, "Your session has expired. Please sign in again")
			return
		}

		ctx := r.Context()
		switch claims.Role {
		case util.RoleUser:
			if id, err := bson.ObjectIDFromHex(claims.UserID); err == nil {
				ctx = context.WithValue(ctx, UserIDKey, id)
			}
		case util.RoleWaiter:
			if id, err := bson.ObjectIDFromHex(claims.WaiterID); err == nil {
				ctx = context.WithValue(ctx, WaiterIDKey, id)
			}
			if id, err := bson.ObjectIDFromHex(claims.RestaurantID); err == nil {
				ctx = context.WithValue(ctx, RestaurantIDKey, id)
			}
		default:
			util.JsonError(w, http.StatusForbidden, "This token cannot be used here")
			return
		}
		ctx = context.WithValue(ctx, RoleKey, claims.Role)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// AuthedUser returns the signed-in diner, if the request carried a user token.
func AuthedUser(r *http.Request) (bson.ObjectID, bool) {
	id, ok := r.Context().Value(UserIDKey).(bson.ObjectID)
	return id, ok
}

// AuthedWaiter returns the signed-in waiter, if the request carried a waiter token.
func AuthedWaiter(r *http.Request) (bson.ObjectID, bool) {
	id, ok := r.Context().Value(WaiterIDKey).(bson.ObjectID)
	return id, ok
}

// Role returns the caller's role, or "" for an anonymous request.
func Role(r *http.Request) string {
	role, _ := r.Context().Value(RoleKey).(string)
	return role
}

// RestaurantID returns the restaurant the caller is scoped to, if any.
func RestaurantID(r *http.Request) (bson.ObjectID, bool) {
	id, ok := r.Context().Value(RestaurantIDKey).(bson.ObjectID)
	return id, ok
}

// AdminID returns the signed-in admin, if any.
func AdminID(r *http.Request) (bson.ObjectID, bool) {
	id, ok := r.Context().Value(AdminIDKey).(bson.ObjectID)
	return id, ok
}
