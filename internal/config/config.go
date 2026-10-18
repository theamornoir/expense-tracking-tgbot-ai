package config

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

// Config holds application configuration loaded from environment variables.
type Config struct {
	TelegramToken  string         `env:"TELEGRAM_TOKEN,required"`
	DatabaseURL    string         `env:"DATABASE_URL,required"`
	LLMBaseURL     string         `env:"LLM_BASE_URL,required"`
	LLMAPIKey      string         `env:"LLM_API_KEY,required"`
	LLMModel       string         `env:"LLM_MODEL,required"`
	AllowedIDs     string         `env:"ALLOWED_USER_IDS,required"`
	AppTimezoneStr string         `env:"APP_TIMEZONE"`
	AllowedUserIDs []int64        `env:"-"`
	AppTimezone    *time.Location `env:"-"`
	MOCK_LLM       bool           `env:"MOCK_LLM"`
}

// Load reads the configuration from environment variables.
func Load() (*Config, error) {
	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("parse env: %w", err)
	}

	ids, err := parseInt64List(cfg.AllowedIDs)
	if err != nil {
		return nil, fmt.Errorf("ALLOWED_USER_IDS: %w", err)
	}
	cfg.AllowedUserIDs = ids

	tzName := cfg.AppTimezoneStr
	if tzName == "" {
		tzName = "Europe/Moscow"
	}
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		return nil, fmt.Errorf("APP_TIMEZONE %q: %w", tzName, err)
	}
	cfg.AppTimezone = loc

	return cfg, nil
}

// parseInt64List splits a comma-separated list of IDs, skipping empty parts.
func parseInt64List(s string) ([]int64, error) {
	var result []int64
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse %q: %w", part, err)
		}
		result = append(result, id)
	}
	if len(result) == 0 {
		return nil, errors.New("at least one user id required")
	}
	return result, nil
}

// IsAllowed reports whether userID is in the allow list.
func (c *Config) IsAllowed(userID int64) bool {
	return slices.Contains(c.AllowedUserIDs, userID)
}
