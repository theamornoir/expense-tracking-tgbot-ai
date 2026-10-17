package config

import (
	"testing"
)

func TestLoad(t *testing.T) {
	t.Setenv("TELEGRAM_TOKEN", "token")
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("LLM_BASE_URL", "https://api.openai.com/v1")
	t.Setenv("LLM_API_KEY", "sk-your-key")
	t.Setenv("LLM_MODEL", "gpt-4o-mini")
	t.Setenv("ALLOWED_USER_IDS", "111,222")
	t.Setenv("APP_TIMEZONE", "Europe/Moscow")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg == nil {
		t.Fatal("cfg must not be nil")
	}
	if len(cfg.AllowedUserIDs) != 2 {
		t.Fatalf("expected 2 allowed ids, got %d", len(cfg.AllowedUserIDs))
	}
	if cfg.AllowedUserIDs[0] != 111 || cfg.AllowedUserIDs[1] != 222 {
		t.Fatalf("unexpected ids: %v", cfg.AllowedUserIDs)
	}
	if cfg.AppTimezone == nil || cfg.AppTimezone.String() != "Europe/Moscow" {
		t.Fatalf("unexpected timezone: %v", cfg.AppTimezone)
	}
	if !cfg.IsAllowed(111) || cfg.IsAllowed(999) {
		t.Fatal("IsAllowed check failed")
	}
}

func TestLoadMissingRequired(t *testing.T) {
	for _, k := range []string{"TELEGRAM_TOKEN", "DATABASE_URL", "LLM_BASE_URL", "LLM_API_KEY", "LLM_MODEL", "ALLOWED_USER_IDS"} {
		t.Setenv(k, "")
	}
	_, err := Load()
	if err == nil {
		t.Fatal("expected error when required env missing")
	}
}
