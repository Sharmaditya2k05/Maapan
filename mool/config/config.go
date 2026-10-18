// Package config reads service configuration from the environment.
//
// A local .env file is loaded first (if present) without overriding variables
// that are already set, so Cloud Run env vars and Secret Manager mounts win.
package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Base is the configuration every Taraju Go service shares.
type Base struct {
	ServiceName   string
	Env           string // local | dev | staging | prod
	Port          string
	DatabaseURL   string
	RedisURL      string
	JWTSecret     string
	ServiceSecret string
	AutoMigrate   bool
	LogLevel      string
	CORSOrigins   []string

	EventsDriver string // direct | pubsub | log
	GCPProject   string
	// UseIDTokens adds a Google-signed ID token to internal calls (Cloud Run with IAM auth).
	UseIDTokens bool
}

// LoadBase loads .env and reads the shared settings. Port defaults to defaultPort
// unless PORT is set (Cloud Run always sets PORT=8080).
func LoadBase(serviceName, defaultPort string) Base {
	LoadDotEnv(".env")
	b := Base{
		ServiceName:   serviceName,
		Env:           String("APP_ENV", "local"),
		Port:          String("PORT", defaultPort),
		DatabaseURL:   String("DATABASE_URL", ""),
		RedisURL:      String("REDIS_URL", ""),
		JWTSecret:     String("JWT_SECRET", ""),
		ServiceSecret: String("SERVICE_SECRET", ""),
		AutoMigrate:   Bool("AUTO_MIGRATE", true),
		LogLevel:      String("LOG_LEVEL", "info"),
		CORSOrigins:   List("CORS_ORIGINS", []string{"http://localhost:3000"}),
		EventsDriver:  String("EVENTS_DRIVER", "direct"),
		GCPProject:    String("GCP_PROJECT", ""),
		UseIDTokens:   Bool("USE_ID_TOKENS", false),
	}
	return b
}

// IsProd reports whether the service runs in production.
func (b Base) IsProd() bool { return b.Env == "prod" }

// Validate fails fast on settings that every service needs outside local dev.
func (b Base) Validate(needDB bool) error {
	var missing []string
	if b.JWTSecret == "" {
		missing = append(missing, "JWT_SECRET")
	}
	if b.ServiceSecret == "" {
		missing = append(missing, "SERVICE_SECRET")
	}
	if needDB && b.DatabaseURL == "" {
		missing = append(missing, "DATABASE_URL")
	}
	if b.EventsDriver == "pubsub" && b.GCPProject == "" {
		missing = append(missing, "GCP_PROJECT")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required env: %s", strings.Join(missing, ", "))
	}
	if b.Env != "local" && len(b.JWTSecret) < 32 {
		return fmt.Errorf("JWT_SECRET must be at least 32 characters outside local")
	}
	return nil
}

// String returns the env var or def when unset/empty.
func String(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

// Int returns the env var parsed as int, or def.
func Int(key string, def int) int {
	if v := String(key, ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// Bool returns the env var parsed as bool, or def.
func Bool(key string, def bool) bool {
	if v := String(key, ""); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

// Duration returns the env var parsed by time.ParseDuration, or def.
func Duration(key string, def time.Duration) time.Duration {
	if v := String(key, ""); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// List returns a comma-separated env var as a trimmed slice, or def.
func List(key string, def []string) []string {
	v := String(key, "")
	if v == "" {
		return def
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// LoadDotEnv reads KEY=VALUE lines from path. Existing env vars are kept.
// Missing file is not an error.
func LoadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 1 && (v[0] == '"' || v[0] == '\'') {
			// Quoted value: keep everything inside the quotes, drop anything after.
			if end := strings.IndexByte(v[1:], v[0]); end >= 0 {
				v = v[1 : end+1]
			}
		} else if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i]) // inline comment
		} else if strings.HasPrefix(v, "#") {
			v = ""
		}
		if _, exists := os.LookupEnv(k); !exists {
			os.Setenv(k, v)
		}
	}
}
