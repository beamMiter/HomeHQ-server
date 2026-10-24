package config

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
)

// minJWTSecretLen is the shortest signing secret accepted in production.
// HS256 wants at least 256 bits; 32 random characters meet that.
const minJWTSecretLen = 32

type Config struct {
	// AppEnv is "production" or anything else (development). Production turns
	// the unsafe development defaults into start-up errors; see Validate.
	AppEnv string

	AppPort    string
	DSN        string
	JWTSecret  string
	CorsOrigin string

	// TrustedProxyHops is how many reverse proxies sit in front of the server
	// and append to X-Forwarded-For (0 = none: use the socket address). Wrong
	// values let a caller pick the IP that rate limits see.
	TrustedProxyHops int

	// AuthRatePerMin caps calls per minute per address on the sign-in routes
	// (login, refresh, forgot/reset password). 0 turns the cap off. Wrong
	// passwords are limited separately and much tighter (user.Throttle).
	AuthRatePerMin int

	StorageBackend string
	LocalDir       string
	PublicBaseURL  string

	// Google Places (optional). Empty key = feature off; the API then answers
	// 503 google_places_disabled and the app falls back to saved contractors.
	GooglePlacesAPIKey  string
	GooglePlacesBaseURL string // empty = Google's production endpoint

	// Bounds on Google traffic. Zero means "use the built-in default".
	GooglePlacesDailyLimit     int  // outbound calls per day, all users
	GooglePlacesRatePerMin     int  // requests per user per minute
	GooglePlacesMaxRadiusM     int  // largest accepted search radius
	GooglePlacesAllowAllTrades bool // permit a search with no trade and no text

	// Cameras (optional). CamerasDriver is "imou", "fake" or empty: empty picks
	// "imou" when both Imou credentials are set, otherwise the feature is off.
	CamerasDriver        string
	CamerasFakeStreamURL string // HLS address the fake driver hands out
	CamerasFakePictures  bool   // fake cameras give a made-up still picture

	ImouAppID     string
	ImouAppSecret string
	ImouBaseURL   string // empty = region default, else Imou's global host
	ImouRegion    string // "sg"; other regions: set ImouBaseURL

	// Appliances (optional). AppliancesDriver is "local" (appliances the
	// household adds itself, simulated here), "fake" (a fixed demo set, for
	// tests) or empty (feature off). Real vendors are added as drivers.
	AppliancesDriver string

	// Cloudflare R2
	R2AccountID string
	R2AccessKey string
	R2SecretKey string
	R2Bucket    string
	R2PublicURL string
}

func Getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func Getbool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

// Getint reads an integer environment variable, falling back to def when it is
// unset or not a number.
func Getint(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// defaultDSN is the local development database. Production refuses it.
const defaultDSN = "postgres://dev:devpass@localhost:5432/homeservice?sslmode=disable"

// IsProduction reports whether APP_ENV=production.
func (c Config) IsProduction() bool {
	return strings.EqualFold(strings.TrimSpace(c.AppEnv), "production")
}

// Validate returns every reason the configuration is unsafe to run. In
// development it finds none; the caller decides whether to stop on a
// production result.
func (c Config) Validate() error {
	if !c.IsProduction() {
		return nil
	}
	var errs []error
	if c.JWTSecret == "change-me" || len(c.JWTSecret) < minJWTSecretLen {
		errs = append(errs, fmt.Errorf("JWT_SECRET must be set to at least %d characters", minJWTSecretLen))
	}
	if c.DSN == defaultDSN {
		errs = append(errs, errors.New("DB_DSN must point at the production database, not the local development one"))
	}
	if c.CorsOrigin == "*" {
		errs = append(errs, errors.New(`CORS_ALLOW_ORIGIN must not be "*" (leave it unset to allow no browser origin)`))
	}
	return errors.Join(errs...)
}

func Load() Config {
	c := Config{
		AppEnv: Getenv("APP_ENV", "development"),
		// Cloud hosts (Cloud Run, Heroku-style platforms) say which port to
		// listen on through PORT; APP_PORT wins when both are set.
		AppPort:          Getenv("APP_PORT", Getenv("PORT", "8080")),
		DSN:              Getenv("DB_DSN", defaultDSN),
		JWTSecret:        Getenv("JWT_SECRET", "change-me"),
		CorsOrigin:       Getenv("CORS_ALLOW_ORIGIN", ""),
		TrustedProxyHops: Getint("TRUSTED_PROXY_HOPS", 0),
		AuthRatePerMin:   Getint("AUTH_RATE_LIMIT_PER_MIN", 60),

		StorageBackend: Getenv("STORAGE_BACKEND", "local"),
		LocalDir:       Getenv("LOCAL_STORAGE_DIR", "./data/uploads"),
		PublicBaseURL:  Getenv("PUBLIC_BASE_URL", "http://localhost:8080/static"),

		GooglePlacesAPIKey:  Getenv("GOOGLE_PLACES_API_KEY", ""),
		GooglePlacesBaseURL: Getenv("GOOGLE_PLACES_BASE_URL", ""),

		GooglePlacesDailyLimit:     Getint("GOOGLE_PLACES_DAILY_LIMIT", 0),
		GooglePlacesRatePerMin:     Getint("GOOGLE_PLACES_RATE_PER_MIN", 0),
		GooglePlacesMaxRadiusM:     Getint("GOOGLE_PLACES_MAX_RADIUS_M", 0),
		GooglePlacesAllowAllTrades: Getbool("GOOGLE_PLACES_ALLOW_ALL_TRADES", false),

		CamerasDriver:        Getenv("CAMERAS_DRIVER", ""),
		CamerasFakeStreamURL: Getenv("CAMERAS_FAKE_STREAM_URL", ""),
		CamerasFakePictures:  Getbool("CAMERAS_FAKE_PICTURES", false),

		ImouAppID:     Getenv("IMOU_APP_ID", ""),
		ImouAppSecret: Getenv("IMOU_APP_SECRET", ""),
		ImouBaseURL:   Getenv("IMOU_BASE_URL", ""),
		ImouRegion:    Getenv("IMOU_REGION", ""),

		AppliancesDriver: Getenv("APPLIANCES_DRIVER", ""),

		R2AccountID: Getenv("R2_ACCOUNT_ID", ""),
		R2AccessKey: Getenv("R2_ACCESS_KEY_ID", ""),
		R2SecretKey: Getenv("R2_SECRET_ACCESS_KEY", ""),
		R2Bucket:    Getenv("R2_BUCKET", ""),
		R2PublicURL: Getenv("R2_PUBLIC_URL", ""),
	}

	// Development keeps the old open default; production allows no browser
	// origin unless one is named (the app is a native client, so it needs none).
	if c.CorsOrigin == "" && !c.IsProduction() {
		c.CorsOrigin = "*"
	}
	if c.JWTSecret == "change-me" {
		log.Println("[WARN] using default JWT secret; set JWT_SECRET (APP_ENV=production refuses to start without one)")
	}

	return c
}
