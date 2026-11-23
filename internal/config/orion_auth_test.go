package config

import "testing"

func TestApplyOrionAuthEnvOverrides(t *testing.T) {
	t.Setenv("ORION_AUTH_ENABLE", "true")
	t.Setenv("ORION_BASE_URL", "http://192.168.3.139:8000/")
	t.Setenv("ORION_AUTH_ME_PATH", "")
	t.Setenv("ORION_TIMEOUT_SECONDS", "8")
	t.Setenv("ORION_AUTH_LOGIN_FALLBACK", "TRUE")

	cfg := &Config{}
	applyOrionAuthEnvOverrides(cfg)

	if !cfg.OrionAuth.Enable {
		t.Fatal("enable = false, want true")
	}
	if cfg.OrionAuth.BaseURL != "http://192.168.3.139:8000" {
		t.Fatalf("base url = %q, want trailing slash trimmed", cfg.OrionAuth.BaseURL)
	}
	if cfg.OrionAuth.MePath != "/orion/v1/iam/me" {
		t.Fatalf("me path = %q, want default", cfg.OrionAuth.MePath)
	}
	if cfg.OrionAuth.TimeoutSeconds != 8 {
		t.Fatalf("timeout = %d, want 8", cfg.OrionAuth.TimeoutSeconds)
	}
	if !cfg.OrionAuth.LoginFallback {
		t.Fatal("login fallback = false, want true")
	}
	if cfg.IsLoginDisabled() {
		t.Fatal("login must stay enabled when the fallback is on")
	}
}

func TestOrionAuthDefaultsKeepLoginEnabled(t *testing.T) {
	t.Setenv("ORION_AUTH_ENABLE", "")
	t.Setenv("ORION_BASE_URL", "")
	t.Setenv("ORION_AUTH_LOGIN_FALLBACK", "")
	t.Setenv("ORION_TIMEOUT_SECONDS", "")

	cfg := &Config{}
	applyOrionAuthEnvOverrides(cfg)

	if cfg.OrionAuth.Enable {
		t.Fatal("orion must be disabled by default")
	}
	if cfg.OrionAuth.TimeoutSeconds != 5 {
		t.Fatalf("timeout = %d, want 5", cfg.OrionAuth.TimeoutSeconds)
	}
	if cfg.IsLoginDisabled() {
		t.Fatal("local login must stay available while orion is disabled")
	}
}

func TestIsLoginDisabledRequiresOrionAndNoFallback(t *testing.T) {
	cases := []struct {
		name     string
		cfg      *Config
		disabled bool
	}{
		{name: "nil config", cfg: nil},
		{name: "no orion block", cfg: &Config{}},
		{name: "orion off", cfg: &Config{OrionAuth: &OrionAuthConfig{Enable: false}}},
		{name: "orion on, fallback on", cfg: &Config{OrionAuth: &OrionAuthConfig{Enable: true, LoginFallback: true}}},
		{name: "orion on, fallback off", cfg: &Config{OrionAuth: &OrionAuthConfig{Enable: true}}, disabled: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.IsLoginDisabled(); got != tc.disabled {
				t.Fatalf("IsLoginDisabled() = %v, want %v", got, tc.disabled)
			}
		})
	}
}

func TestValidateConfigRequiresOrionBaseURL(t *testing.T) {
	cfg := &Config{OrionAuth: &OrionAuthConfig{Enable: true}}
	if err := ValidateConfig(cfg); err == nil {
		t.Fatal("expected validation error for a missing base url")
	}

	cfg.OrionAuth.BaseURL = "http://192.168.3.139:8000"
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}
