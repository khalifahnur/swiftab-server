package util

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	RoleAdmin  = "admin"
	RoleWaiter = "waiter"
	RoleUser   = "user"
	// RoleReset marks a short-lived token that only authorises a password reset.
	RoleReset = "password_reset"
)

func getJWTSecret() []byte {
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		// Fallback for local development. Set JWT_SECRET in production.
		return []byte("change-me-in-production")
	}
	return []byte(secret)
}

type Claims struct {
	AdminID      string `json:"adminId,omitempty"`
	RestaurantID string `json:"restaurantId,omitempty"`
	Email        string `json:"email,omitempty"`
	Role         string `json:"role,omitempty"`
	UserID       string `json:"userId,omitempty"`
	WaiterID     string `json:"waiterId,omitempty"`
	jwt.RegisteredClaims
}

func sign(claims Claims, expiry time.Duration) (string, error) {
	now := time.Now()
	claims.RegisteredClaims = jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(now.Add(expiry)),
		IssuedAt:  jwt.NewNumericDate(now),
		Issuer:    "swiftab",
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(getJWTSecret())
}

// GenerateToken issues a token for a restaurant admin (role "admin") or, for
// backwards compatibility, any other restaurant-scoped role.
func GenerateToken(adminID, restaurantID, email, role string, expiry time.Duration) (string, error) {
	return sign(Claims{
		AdminID:      adminID,
		RestaurantID: restaurantID,
		Email:        email,
		Role:         role,
	}, expiry)
}

func GenerateWaiterToken(waiterID, restaurantID, email string, expiry time.Duration) (string, error) {
	return sign(Claims{
		WaiterID:     waiterID,
		RestaurantID: restaurantID,
		Email:        email,
		Role:         RoleWaiter,
	}, expiry)
}

func GenerateUserToken(userID, email string, expiry time.Duration) (string, error) {
	return sign(Claims{
		UserID: userID,
		Email:  email,
		Role:   RoleUser,
	}, expiry)
}

// GenerateResetToken issues a token proving the holder verified the emailed
// reset code for this address.
func GenerateResetToken(email string, expiry time.Duration) (string, error) {
	return sign(Claims{Email: email, Role: RoleReset}, expiry)
}

func ParseToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return getJWTSecret(), nil
	})

	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		return claims, nil
	}

	return nil, errors.New("invalid or expired token")
}
