package util

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/cloudinary/cloudinary-go/v2"
	"github.com/cloudinary/cloudinary-go/v2/api/uploader"
	"golang.org/x/crypto/bcrypt"
)

func JsonResponse(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// JsonError writes the failure under both "error" and "message": the web
// dashboard and the mobile apps historically read different keys.
func JsonError(w http.ResponseWriter, status int, message string) {
	JsonResponse(w, status, map[string]string{"error": message, "message": message})
}

// DecodeJSON reads a JSON request body of at most 1 MB into dst.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst interface{}) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(dst)
}

// bcryptCost balances login latency on small instances against brute-force
// resistance. Existing hashes created with another cost keep verifying.
const bcryptCost = 12

func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	return string(bytes), err
}

func VerifyPassword(hashedPassword, password string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(password))
	return err == nil
}

// NormalizeEmail trims and lowercases an address and reports whether it looks
// like a deliverable email.
func NormalizeEmail(email string) (string, bool) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || len(email) > 254 {
		return email, false
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || !strings.Contains(email[strings.LastIndex(email, "@")+1:], ".") {
		return email, false
	}
	return email, true
}

// GenerateCode returns a random 6-digit numeric code.
func GenerateCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(900000))
	if err != nil {
		return fmt.Sprintf("%06d", time.Now().UnixNano()%900000+100000)
	}
	return fmt.Sprintf("%d", n.Int64()+100000)
}

var slugCleaner = regexp.MustCompile(`[^a-z0-9]+`)

// categoryKeyPattern is deliberately strict: keys are interpolated into MongoDB
// update paths, so "." and "$" must never get through.
var categoryKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// Slugify turns a display label such as "Soft Drinks" into "soft-drinks".
func Slugify(label string) string {
	s := slugCleaner.ReplaceAllString(strings.ToLower(strings.TrimSpace(label)), "-")
	s = strings.Trim(s, "-")
	if len(s) > 32 {
		s = strings.Trim(s[:32], "-")
	}
	return s
}

func ValidCategoryKey(key string) bool {
	return categoryKeyPattern.MatchString(key)
}

// NormalizePaymentMethod maps the labels the clients send ("M-Pesa", "Cash",
// "Credit Card"...) onto the canonical keys stored on orders.
func NormalizePaymentMethod(method string) string {
	m := strings.ToLower(strings.TrimSpace(method))
	m = strings.NewReplacer("-", "", "_", "", " ", "").Replace(m)
	switch m {
	case "":
		return ""
	case "mpesa", "mobilemoney", "mobile":
		return "mpesa"
	case "cash":
		return "cash"
	case "card", "creditcard", "debitcard", "visa", "mastercard":
		return "card"
	case "airtel", "airtelmoney":
		return "airtel_money"
	default:
		return m
	}
}

type CloudinaryService struct {
	client *cloudinary.Cloudinary
}

func NewCloudinaryService(cloudName, apiKey, apiSecret string) (*CloudinaryService, error) {
	if cloudName == "" || apiKey == "" || apiSecret == "" {
		return nil, errors.New("cloudinary credentials are not set")
	}
	cld, err := cloudinary.NewFromParams(cloudName, apiKey, apiSecret)
	if err != nil {
		return nil, err
	}

	return &CloudinaryService{
		client: cld,
	}, nil
}

// Upload stores an image in the given Cloudinary folder and returns its URL.
func (s *CloudinaryService) Upload(ctx context.Context, file io.Reader, folder string) (string, error) {
	if s == nil || s.client == nil {
		return "", errors.New("image storage is not configured")
	}
	resp, err := s.client.Upload.Upload(ctx, file, uploader.UploadParams{
		Folder: folder,
	})
	if err != nil {
		return "", err
	}
	if resp.Error.Message != "" {
		return "", errors.New(resp.Error.Message)
	}
	if resp.SecureURL == "" {
		return "", errors.New("image upload returned no URL")
	}
	return resp.SecureURL, nil
}

func (s *CloudinaryService) UploadRestaurantImage(ctx context.Context, file io.Reader) (string, error) {
	return s.Upload(ctx, file, "restaurants")
}

func (s *CloudinaryService) UploadMenuImage(ctx context.Context, file io.Reader) (string, error) {
	return s.Upload(ctx, file, "menu")
}

func GenerateOrderID(restaurantID string) string {
	suffix := "XXXXXX"
	if len(restaurantID) >= 6 {
		suffix = strings.ToUpper(restaurantID[len(restaurantID)-6:])
	}
	n, err := rand.Int(rand.Reader, big.NewInt(900))
	extra := int64(0)
	if err == nil {
		extra = n.Int64()
	}
	return fmt.Sprintf("ORD-%s-%d%03d", suffix, time.Now().Unix()%1000000, extra+100)
}
