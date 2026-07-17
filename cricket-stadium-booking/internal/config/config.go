// Package config loads and validates environment configuration.
// TTL validation is a hard startup requirement (stress-test gap #6): a
// misconfigured TTL of 0 or negative would silently defeat holds entirely.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	DatabaseURL      string
	HoldTTL          time.Duration
	PoolMaxConns     int32
	RequestTimeout   time.Duration
	OutboxBatchSize  int
	MaxRetries       int
	RateLimitEnabled bool
}

func Load() (Config, error) {
	c := Config{
		DatabaseURL:     getEnv("DATABASE_URL", "postgres:///cricket_stadium_booking?host=/tmp"),
		HoldTTL:         getDuration("HOLD_TTL", 5*time.Minute),
		PoolMaxConns:    int32(getInt("POOL_MAX_CONNS", 20)),
		RequestTimeout:  getDuration("REQUEST_TIMEOUT", 2*time.Second),
		OutboxBatchSize: getInt("OUTBOX_BATCH_SIZE", 100),
		MaxRetries:      getInt("MAX_RETRIES", 3),
		// Off by default: this build's own load-test harness IS a
		// legitimate single-IP high-volume client (localhost), and an
		// IP-based limiter cannot distinguish it from an attacker doing the
		// same thing (CODE_REVIEW.md finding #7's fix conflicted with the
		// Definition of Done proof when both were tested empirically —
		// see erd.md's build-time-corrections). Real deployments (anything
		// not run against localhost by the operator) should set this true.
		RateLimitEnabled: getBool("RATE_LIMIT_ENABLED", false),
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Validate fails fast on any config value that would silently defeat a
// correctness or safety guarantee elsewhere in the system.
func (c Config) Validate() error {
	if c.HoldTTL <= 0 {
		return fmt.Errorf("config: HOLD_TTL must be > 0, got %s", c.HoldTTL)
	}
	if c.HoldTTL > time.Hour {
		return fmt.Errorf("config: HOLD_TTL %s exceeds sane upper bound (1h)", c.HoldTTL)
	}
	if c.PoolMaxConns <= 0 {
		return fmt.Errorf("config: POOL_MAX_CONNS must be > 0, got %d", c.PoolMaxConns)
	}
	if c.RequestTimeout <= 0 {
		return fmt.Errorf("config: REQUEST_TIMEOUT must be > 0, got %s", c.RequestTimeout)
	}
	if c.OutboxBatchSize <= 0 {
		return fmt.Errorf("config: OUTBOX_BATCH_SIZE must be > 0, got %d", c.OutboxBatchSize)
	}
	if c.MaxRetries < 0 {
		return fmt.Errorf("config: MAX_RETRIES must be >= 0, got %d", c.MaxRetries)
	}
	return nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func getBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

func getInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
