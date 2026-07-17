package config

import (
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	base := Config{
		HoldTTL:         5 * time.Minute,
		PoolMaxConns:    20,
		RequestTimeout:  2 * time.Second,
		OutboxBatchSize: 100,
		MaxRetries:      3,
	}

	tests := []struct {
		name    string
		mutate  func(c Config) Config
		wantErr bool
	}{
		{"valid defaults", func(c Config) Config { return c }, false},
		{"zero TTL rejected", func(c Config) Config { c.HoldTTL = 0; return c }, true},
		{"negative TTL rejected", func(c Config) Config { c.HoldTTL = -1; return c }, true},
		{"TTL over 1h rejected", func(c Config) Config { c.HoldTTL = 2 * time.Hour; return c }, true},
		{"TTL exactly 1h allowed", func(c Config) Config { c.HoldTTL = time.Hour; return c }, false},
		{"zero pool conns rejected", func(c Config) Config { c.PoolMaxConns = 0; return c }, true},
		{"zero request timeout rejected", func(c Config) Config { c.RequestTimeout = 0; return c }, true},
		{"zero outbox batch rejected", func(c Config) Config { c.OutboxBatchSize = 0; return c }, true},
		{"negative max retries rejected", func(c Config) Config { c.MaxRetries = -1; return c }, true},
		{"zero max retries allowed", func(c Config) Config { c.MaxRetries = 0; return c }, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.mutate(base).Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() err = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}
