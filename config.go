package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// AccountOverride holds sparse per-account policy fields. A nil field means
// "inherit the global default"; an explicitly set field (including an explicit
// false for the boolean) is a real override that survives global changes.
// Overrides are keyed by stable account identity (auth_index), not labels.
type AccountOverride struct {
	RefreshInterval          *time.Duration `yaml:"refresh_interval" json:"-"`
	TriggerLeadTime          *time.Duration `yaml:"trigger_lead_time" json:"-"`
	AutoResetOnWeeklyExhaust *bool          `yaml:"auto_reset_on_weekly_exhaustion" json:"auto_reset_on_weekly_exhaustion,omitempty"`
}

// IsZero reports whether every field is unset (pure inheritance). Empty
// override entries can be dropped from the saved map.
func (o AccountOverride) IsZero() bool {
	return o.RefreshInterval == nil && o.TriggerLeadTime == nil && o.AutoResetOnWeeklyExhaust == nil
}

// MarshalJSON renders duration overrides as strings and keeps nil fields
// absent so "inherit" round-trips as absence, not a copied value.
func (o AccountOverride) MarshalJSON() ([]byte, error) {
	type alias struct {
		RefreshInterval          *string `json:"refresh_interval,omitempty"`
		TriggerLeadTime          *string `json:"trigger_lead_time,omitempty"`
		AutoResetOnWeeklyExhaust *bool   `json:"auto_reset_on_weekly_exhaustion,omitempty"`
	}
	var a alias
	if o.RefreshInterval != nil {
		s := formatDuration(*o.RefreshInterval)
		a.RefreshInterval = &s
	}
	if o.TriggerLeadTime != nil {
		s := formatDuration(*o.TriggerLeadTime)
		a.TriggerLeadTime = &s
	}
	a.AutoResetOnWeeklyExhaust = o.AutoResetOnWeeklyExhaust
	return json.Marshal(a)
}

// UnmarshalJSON accepts duration strings or nanosecond integers, mirroring
// the Config readers. Explicit false for the boolean is preserved as a
// pointer so it stays distinguishable from "absent".
func (o *AccountOverride) UnmarshalJSON(raw []byte) error {
	type alias struct {
		RefreshInterval          json.RawMessage `json:"refresh_interval"`
		TriggerLeadTime          json.RawMessage `json:"trigger_lead_time"`
		AutoResetOnWeeklyExhaust *bool           `json:"auto_reset_on_weekly_exhaustion"`
	}
	var a alias
	if err := json.Unmarshal(raw, &a); err != nil {
		return err
	}
	o.AutoResetOnWeeklyExhaust = a.AutoResetOnWeeklyExhaust
	if len(bytes.TrimSpace(a.RefreshInterval)) > 0 {
		d, err := parseDurationJSON(a.RefreshInterval)
		if err != nil {
			return fmt.Errorf("refresh_interval: %w", err)
		}
		o.RefreshInterval = &d
	} else {
		o.RefreshInterval = nil
	}
	if len(bytes.TrimSpace(a.TriggerLeadTime)) > 0 {
		d, err := parseDurationJSON(a.TriggerLeadTime)
		if err != nil {
			return fmt.Errorf("trigger_lead_time: %w", err)
		}
		o.TriggerLeadTime = &d
	} else {
		o.TriggerLeadTime = nil
	}
	return nil
}

// EffectivePolicy is the fully-resolved per-account policy. The worker and
// the UI both use it so they cannot implement different inheritance rules.
type EffectivePolicy struct {
	RefreshInterval          time.Duration `json:"-"`
	TriggerLeadTime          time.Duration `json:"-"`
	AutoResetOnWeeklyExhaust bool          `json:"auto_reset_on_weekly_exhaustion"`
}

// Config holds the user-tunable fields. The scheduling fields are editable
// from the management UI; state_dir is an operator-only override that comes
// from CPA's config_yaml. All other behavior is hardcoded in constants.go —
// endpoints, retry delays, post-reset delay, log limits. Endpoints and OpenAI
// parameters are NEVER user-configurable.
type Config struct {
	RefreshInterval          time.Duration              `yaml:"refresh_interval" json:"-"`
	TriggerLeadTime          time.Duration              `yaml:"trigger_lead_time" json:"-"`
	EnabledAccounts          []string                   `yaml:"enabled_accounts" json:"enabled_accounts"`
	StateDir                 string                     `yaml:"state_dir" json:"state_dir,omitempty"`
	AutoResetOnWeeklyExhaust bool                       `yaml:"auto_reset_on_weekly_exhaustion" json:"auto_reset_on_weekly_exhaustion"`
	AccountOverrides         map[string]AccountOverride `yaml:"account_overrides" json:"account_overrides,omitempty"`
}

// EffectivePolicy resolves one account's policy field-by-field: a saved
// override wins; an absent field inherits the global default.
func (c Config) EffectivePolicy(authID string) EffectivePolicy {
	p := EffectivePolicy{
		RefreshInterval:          c.RefreshInterval,
		TriggerLeadTime:          c.TriggerLeadTime,
		AutoResetOnWeeklyExhaust: c.AutoResetOnWeeklyExhaust,
	}
	if ov, ok := c.AccountOverrides[authID]; ok {
		if ov.RefreshInterval != nil {
			p.RefreshInterval = *ov.RefreshInterval
		}
		if ov.TriggerLeadTime != nil {
			p.TriggerLeadTime = *ov.TriggerLeadTime
		}
		if ov.AutoResetOnWeeklyExhaust != nil {
			p.AutoResetOnWeeklyExhaust = *ov.AutoResetOnWeeklyExhaust
		}
	}
	return p
}

// MarshalJSON renders Duration fields as human-readable strings ("1h",
// "5m", "1h30m") instead of Go's default nanosecond integer, which is
// unreadable in the management UI's raw JSON view.
func (c Config) MarshalJSON() ([]byte, error) {
	type alias struct {
		RefreshInterval          string                     `json:"refresh_interval"`
		TriggerLeadTime          string                     `json:"trigger_lead_time"`
		EnabledAccounts          []string                   `json:"enabled_accounts"`
		StateDir                 string                     `json:"state_dir,omitempty"`
		AutoResetOnWeeklyExhaust bool                       `json:"auto_reset_on_weekly_exhaustion"`
		AccountOverrides         map[string]AccountOverride `json:"account_overrides,omitempty"`
	}
	return json.Marshal(alias{
		RefreshInterval:          formatDuration(c.RefreshInterval),
		TriggerLeadTime:          formatDuration(c.TriggerLeadTime),
		EnabledAccounts:          c.EnabledAccounts,
		StateDir:                 c.StateDir,
		AutoResetOnWeeklyExhaust: c.AutoResetOnWeeklyExhaust,
		AccountOverrides:         c.AccountOverrides,
	})
}

// formatDuration trims Go's verbose "12h0m0s" form to the minimal "12h".
// "1h30m0s" -> "1h30m", "90m0s" -> "90m", "45s" stays. Mirrors the scheduler
// plugin's formatDuration so duration displays are consistent across plugins.
func formatDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = s[:len(s)-2]
	}
	if strings.HasSuffix(s, "h0m") {
		s = s[:len(s)-2]
	}
	return s
}

// UnmarshalJSON accepts both the string form ("1h") and the raw nanosecond
// integer form (3600000000000) for Duration fields, for round-trip safety.
// Absent fields decode to zero, which normalizeConfig then repairs to the
// fresh defaults — legacy saved values are never rewritten here.
func (c *Config) UnmarshalJSON(raw []byte) error {
	type alias struct {
		RefreshInterval          json.RawMessage            `json:"refresh_interval"`
		TriggerLeadTime          json.RawMessage            `json:"trigger_lead_time"`
		EnabledAccounts          []string                   `json:"enabled_accounts"`
		StateDir                 string                     `json:"state_dir"`
		AutoResetOnWeeklyExhaust *bool                      `json:"auto_reset_on_weekly_exhaustion"`
		AccountOverrides         map[string]AccountOverride `json:"account_overrides"`
	}
	var a alias
	if err := json.Unmarshal(raw, &a); err != nil {
		return err
	}
	c.EnabledAccounts = a.EnabledAccounts
	c.StateDir = a.StateDir
	c.AccountOverrides = a.AccountOverrides
	if a.AutoResetOnWeeklyExhaust != nil {
		c.AutoResetOnWeeklyExhaust = *a.AutoResetOnWeeklyExhaust
	} else {
		c.AutoResetOnWeeklyExhaust = false
	}
	d1, err := parseDurationJSON(a.RefreshInterval)
	if err != nil {
		return fmt.Errorf("refresh_interval: %w", err)
	}
	c.RefreshInterval = d1
	d2, err := parseDurationJSON(a.TriggerLeadTime)
	if err != nil {
		return fmt.Errorf("trigger_lead_time: %w", err)
	}
	c.TriggerLeadTime = d2
	return nil
}

// parseDurationJSON accepts a JSON string ("1h") or a JSON number (nanoseconds).
func parseDurationJSON(raw json.RawMessage) (time.Duration, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return 0, nil
	}
	// String form: "1h", "1h30m", etc.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return 0, nil
		}
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, fmt.Errorf("parse %q: %w", s, err)
		}
		return d, nil
	}
	// Number form: nanoseconds (Go default).
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return time.Duration(n), nil
	}
	return 0, fmt.Errorf("must be a duration string like \"1h\" or a nanosecond integer")
}

// DefaultConfig returns the spec defaults for a fresh install: 1h patrol,
// 5m credit-expiry lead time, weekly-exhaustion redemption disabled, and no
// accounts enabled (empty == soft-disable until the user opts accounts in).
func DefaultConfig() Config {
	return Config{
		RefreshInterval:          time.Hour,
		TriggerLeadTime:          5 * time.Minute,
		EnabledAccounts:          nil,
		AutoResetOnWeeklyExhaust: false,
	}
}

// parseConfigYAML parses user YAML and normalizes it. Malformed YAML returns
// an error; missing fields fall back to defaults via normalizeConfig.
func parseConfigYAML(raw []byte) (Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config yaml: %w", err)
	}
	return normalizeConfig(cfg), nil
}

// normalizeConfig applies floors and de-dup. Intervals below 1 minute are
// raised to the fresh defaults (1h / 5m) — the floor repairs absent or
// degenerate values; a saved 12h/6h from an older install passes through
// unchanged. Empty/duplicate account IDs are dropped. Account overrides are
// validated field-wise; an all-empty entry is removed so "cleared" means
// inheritance rather than a meaningless record.
func normalizeConfig(cfg Config) Config {
	if cfg.RefreshInterval < time.Minute {
		cfg.RefreshInterval = time.Hour
	}
	if cfg.TriggerLeadTime < time.Minute {
		cfg.TriggerLeadTime = 5 * time.Minute
	}
	out := make([]string, 0, len(cfg.EnabledAccounts))
	seen := map[string]bool{}
	for _, a := range cfg.EnabledAccounts {
		a = strings.TrimSpace(a)
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	cfg.EnabledAccounts = out
	if len(cfg.AccountOverrides) > 0 {
		clean := make(map[string]AccountOverride, len(cfg.AccountOverrides))
		for id, ov := range cfg.AccountOverrides {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if ov.RefreshInterval != nil && *ov.RefreshInterval < time.Minute {
				v := time.Minute
				ov.RefreshInterval = &v
			}
			if ov.TriggerLeadTime != nil && *ov.TriggerLeadTime < time.Minute {
				v := time.Minute
				ov.TriggerLeadTime = &v
			}
			if ov.IsZero() {
				continue
			}
			clean[id] = ov
		}
		if len(clean) == 0 {
			cfg.AccountOverrides = nil
		} else {
			cfg.AccountOverrides = clean
		}
	}
	return cfg
}

// validateSettings rejects newly supplied policy that normalizeConfig would
// otherwise silently rewrite: every duration must be at least one minute.
func validateSettings(cfg Config) error {
	if cfg.RefreshInterval < time.Minute {
		return fmt.Errorf("refresh_interval must be at least 1m")
	}
	if cfg.TriggerLeadTime < time.Minute {
		return fmt.Errorf("trigger_lead_time must be at least 1m")
	}
	for id, ov := range cfg.AccountOverrides {
		if ov.RefreshInterval != nil && *ov.RefreshInterval < time.Minute {
			return fmt.Errorf("account %s: refresh_interval must be at least 1m", id)
		}
		if ov.TriggerLeadTime != nil && *ov.TriggerLeadTime < time.Minute {
			return fmt.Errorf("account %s: trigger_lead_time must be at least 1m", id)
		}
	}
	return nil
}
