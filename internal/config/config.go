package config

import (
	"log"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	MongoURI      string
	Port          string
	Cld           string
	SecretKey     string
	CldApiSk      string
	CldApiKey     string
	CldName       string
	AllowedOrigin string
}

func LoadConfig() *Config {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, relying on system environment variables")
	}

	return &Config{
		MongoURI:      os.Getenv("MONGODB_CONN"),
		Port:          os.Getenv("PORT"),
		SecretKey:     os.Getenv("SESSION_SECRET"),
		CldApiKey:     os.Getenv("CLOUDINARY_API_KEY"),
		CldApiSk:      os.Getenv("CLOUDINARY_API_SECRET"),
		CldName:       os.Getenv("CLOUDINARY_CLOUD_NAME"),
		AllowedOrigin: os.Getenv("ALLOWED_ORIGIN"),
	}
}
