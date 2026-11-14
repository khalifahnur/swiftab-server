package config

import (
	"log"
	"os"
	"strings"
	"time"

	// Embed the timezone database so reporting works in minimal containers.
	_ "time/tzdata"

	"github.com/joho/godotenv"
)

type SMTPConfig struct {
	Host string
	Port string
	User string
	Pass string
	From string
}

// Enabled reports whether outbound email is configured.
func (s SMTPConfig) Enabled() bool {
	return s.Host != "" && s.From != ""
}

type Config struct {
	MongoURI  string
	DBName    string
	Port      string
	SecretKey string
	CldApiSk  string
	CldApiKey string
	CldName   string

	// AllowedOrigins are extra browser origins allowed by CORS (comma separated
	// in ALLOWED_ORIGIN). The swiftab.co.ke origins are always allowed.
	AllowedOrigins []string

	// Env is "production" unless APP_ENV says otherwise. It controls cookie
	// security attributes.
	Env          string
	CookieDomain string

	// Location is the business timezone used for "today" and daily reports.
	Location *time.Location

	// RequireMobileAuth rejects user/waiter requests that carry no bearer
	// token. Leave it off until the app builds that send tokens are released.
	RequireMobileAuth bool

	SMTP SMTPConfig
}

func (c *Config) IsProd() bool {
	return c.Env == "production"
}

func getenv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func LoadConfig() *Config {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, relying on system environment variables")
	}

	env := strings.ToLower(getenv("APP_ENV", "production"))

	cookieDomain := os.Getenv("COOKIE_DOMAIN")
	if cookieDomain == "" && env == "production" {
		cookieDomain = ".swiftab.co.ke"
	}

	var origins []string
	for _, o := range strings.Split(os.Getenv("ALLOWED_ORIGIN"), ",") {
		if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" {
			origins = append(origins, o)
		}
	}

	tzName := getenv("APP_TIMEZONE", "Africa/Nairobi")
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		log.Printf("Unknown APP_TIMEZONE %q, falling back to UTC", tzName)
		loc = time.UTC
	}

	return &Config{
		MongoURI:          os.Getenv("MONGODB_CONN"),
		DBName:            getenv("MONGODB_DB", "test"),
		Port:              getenv("PORT", "8080"),
		SecretKey:         os.Getenv("SESSION_SECRET"),
		CldApiKey:         os.Getenv("CLOUDINARY_API_KEY"),
		CldApiSk:          os.Getenv("CLOUDINARY_API_SECRET"),
		CldName:           os.Getenv("CLOUDINARY_CLOUD_NAME"),
		AllowedOrigins:    origins,
		Env:               env,
		CookieDomain:      cookieDomain,
		Location:          loc,
		RequireMobileAuth: strings.EqualFold(os.Getenv("REQUIRE_MOBILE_AUTH"), "true"),
		SMTP: SMTPConfig{
			Host: os.Getenv("SMTP_HOST"),
			Port: getenv("SMTP_PORT", "587"),
			User: os.Getenv("SMTP_USER"),
			Pass: os.Getenv("SMTP_PASS"),
			From: os.Getenv("SMTP_FROM"),
		},
	}
}
