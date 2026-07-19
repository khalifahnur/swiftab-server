package util

import (
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
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

func JsonError(w http.ResponseWriter, status int, message string) {
	JsonResponse(w, status, map[string]string{"error": message})
}

func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), 14)
	return string(bytes), err
}

func VerifyPassword(hashedPassword, password string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(password))
	return err == nil
}

type CloudinaryService struct {
	client *cloudinary.Cloudinary
}

// NewCloudinaryService now accepts the three separate config values
func NewCloudinaryService(cloudName, apiKey, apiSecret string) (*CloudinaryService, error) {
	// Use NewFromParams to manually pass the three credentials
	cld, err := cloudinary.NewFromParams(cloudName, apiKey, apiSecret)
	if err != nil {
		return nil, err
	}

	return &CloudinaryService{
		client: cld,
	}, nil
}

func (s *CloudinaryService) UploadRestaurantImage(ctx context.Context, file multipart.File) (string, error) {
	resp, err := s.client.Upload.Upload(ctx, file, uploader.UploadParams{
		Folder: "restaurants",
	})
	if err != nil {
		return "", err
	}
	return resp.SecureURL, nil
}

func (s *CloudinaryService) UploadMenuImage(ctx context.Context, file multipart.File) (string, error) {
	resp, err := s.client.Upload.Upload(ctx, file, uploader.UploadParams{
		Folder: "menu",
	})
	if err != nil {
		return "", err
	}
	return resp.SecureURL, nil
}

func GenerateOrderID(restaurantID string) string {
	if len(restaurantID) < 6 {
		return fmt.Sprintf("ORD-XXXX-%d", time.Now().Unix())
	}
	return fmt.Sprintf("ORD-%s-%d", restaurantID[len(restaurantID)-6:], time.Now().Unix())
}
