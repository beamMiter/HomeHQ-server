package config

import (
	"strings"
	"testing"
)

func prod() Config {
	return Config{
		AppEnv:    "production",
		JWTSecret: strings.Repeat("s", 40),
		DSN:       "postgres://u:p@db.example.test/app?sslmode=require",
	}
}

func TestValidateAcceptsAGoodProductionConfig(t *testing.T) {
	if err := prod().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestValidateRefusesUnsafeProductionConfig(t *testing.T) {
	cases := map[string]func(*Config){
		"default secret":  func(c *Config) { c.JWTSecret = "change-me" },
		"short secret":    func(c *Config) { c.JWTSecret = strings.Repeat("s", minJWTSecretLen-1) },
		"empty secret":    func(c *Config) { c.JWTSecret = "" },
		"local dev dsn":   func(c *Config) { c.DSN = defaultDSN },
		"wildcard origin": func(c *Config) { c.CorsOrigin = "*" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := prod()
			mutate(&c)
			if err := c.Validate(); err == nil {
				t.Fatal("Validate() = nil, want an error")
			}
		})
	}
}

func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	c := Config{AppEnv: "production", JWTSecret: "change-me", DSN: defaultDSN, CorsOrigin: "*"}
	err := c.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want errors")
	}
	for _, want := range []string{"JWT_SECRET", "DB_DSN", "CORS_ALLOW_ORIGIN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func TestValidateIgnoresDevelopment(t *testing.T) {
	c := Config{AppEnv: "development", JWTSecret: "change-me", DSN: defaultDSN, CorsOrigin: "*"}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil in development", err)
	}
}

func TestIsProductionIgnoresCaseAndSpace(t *testing.T) {
	for _, env := range []string{"production", "Production", " PRODUCTION "} {
		if !(Config{AppEnv: env}).IsProduction() {
			t.Errorf("IsProduction(%q) = false", env)
		}
	}
	for _, env := range []string{"", "development", "prod", "staging"} {
		if (Config{AppEnv: env}).IsProduction() {
			t.Errorf("IsProduction(%q) = true", env)
		}
	}
}

func TestLoadPortPrefersAppPortThenPort(t *testing.T) {
	t.Setenv("APP_PORT", "")
	t.Setenv("PORT", "9090")
	if got := Load().AppPort; got != "9090" {
		t.Errorf("AppPort = %q, want 9090 from PORT", got)
	}
	t.Setenv("APP_PORT", "7000")
	if got := Load().AppPort; got != "7000" {
		t.Errorf("AppPort = %q, want 7000 from APP_PORT", got)
	}
}

func TestLoadCorsDefaults(t *testing.T) {
	t.Setenv("CORS_ALLOW_ORIGIN", "")
	t.Setenv("APP_ENV", "development")
	if got := Load().CorsOrigin; got != "*" {
		t.Errorf("development CorsOrigin = %q, want *", got)
	}
	t.Setenv("APP_ENV", "production")
	if got := Load().CorsOrigin; got != "" {
		t.Errorf("production CorsOrigin = %q, want empty", got)
	}
}
