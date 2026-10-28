package plugin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.RefreshInterval != time.Hour {
		t.Fatalf("R = %v", cfg.RefreshInterval)
	}
	if cfg.TriggerLeadTime != 5*time.Minute {
		t.Fatalf("L = %v", cfg.TriggerLeadTime)
	}
	if cfg.AutoResetOnWeeklyExhaust {
		t.Fatal("weekly-exhaustion must default off")
	}
	if len(cfg.EnabledAccounts) != 0 {
		t.Fatalf("enabled = %v", cfg.EnabledAccounts)
	}
}

func TestParseConfigYAML(t *testing.T) {
	raw := []byte(`
refresh_interval: 4h
trigger_lead_time: 2h
enabled_accounts: [a, b]
`)
	cfg, err := parseConfigYAML(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.RefreshInterval != 4*time.Hour || cfg.TriggerLeadTime != 2*time.Hour {
		t.Fatalf("parsed = %+v", cfg)
	}
	if len(cfg.EnabledAccounts) != 2 || cfg.EnabledAccounts[0] != "a" {
		t.Fatalf("enabled = %v", cfg.EnabledAccounts)
	}
}

func TestParseConfigYAML_Empty(t *testing.T) {
	cfg, err := parseConfigYAML([]byte(``))
	if err != nil {
		t.Fatalf("parse empty: %v", err)
	}
	// Empty YAML should yield defaults (normalization kicks in).
	if cfg.RefreshInterval != time.Hour {
		t.Fatalf("empty R should default, got %v", cfg.RefreshInterval)
	}
	if cfg.TriggerLeadTime != 5*time.Minute {
		t.Fatalf("empty L should default, got %v", cfg.TriggerLeadTime)
	}
}

func TestNormalizeConfig_FloorsIntervals(t *testing.T) {
	cfg := Config{RefreshInterval: 1 * time.Second, TriggerLeadTime: 0, EnabledAccounts: []string{"x"}}
	cfg = normalizeConfig(cfg)
	// Floor intervals to 1 minute to avoid tight loops.
	if cfg.RefreshInterval < time.Minute {
		t.Fatalf("R not floored: %v", cfg.RefreshInterval)
	}
	if cfg.TriggerLeadTime < time.Minute {
		t.Fatalf("L not floored: %v", cfg.TriggerLeadTime)
	}
}

func TestNormalizeConfig_DedupAndTrimAccounts(t *testing.T) {
	cfg := Config{
		RefreshInterval: 12 * time.Hour,
		TriggerLeadTime: 6 * time.Hour,
		EnabledAccounts: []string{"  a  ", "b", "a", "", "c"},
	}
	cfg = normalizeConfig(cfg)
	want := []string{"a", "b", "c"}
	if len(cfg.EnabledAccounts) != len(want) {
		t.Fatalf("enabled = %v, want %v", cfg.EnabledAccounts, want)
	}
	for i, w := range want {
		if cfg.EnabledAccounts[i] != w {
			t.Fatalf("enabled[%d] = %q, want %q (full: %v)", i, cfg.EnabledAccounts[i], w, cfg.EnabledAccounts)
		}
	}
}

func TestParseConfigYAML_Malformed(t *testing.T) {
	_, err := parseConfigYAML([]byte(`not: [valid: yaml`))
	if err == nil {
		t.Fatalf("expected error on malformed YAML")
	}
}

// TestConfig_MarshalJSON_ProducesDurationStrings verifies the management UI's
// status JSON shows "12h" instead of the raw nanosecond integer
// (43200000000000). Regression guard for the unreadable-JSON bug.
func TestConfig_MarshalJSON_ProducesDurationStrings(t *testing.T) {
	cfg := Config{RefreshInterval: 12 * time.Hour, TriggerLeadTime: 6 * time.Hour, EnabledAccounts: []string{"a"}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)
	for _, want := range []string{`"refresh_interval":"12h"`, `"trigger_lead_time":"6h"`, `"enabled_accounts":["a"]`} {
		if !strings.Contains(body, want) {
			t.Fatalf("JSON missing %q; got %s", want, body)
		}
	}
	if strings.Contains(body, "43200000000000") {
		t.Fatalf("JSON leaked nanosecond integer: %s", body)
	}
}

// TestConfig_UnmarshalJSON_AcceptsStringAndNanoseconds verifies round-trip
// from both the human string form and Go's default nanosecond integer form.
func TestConfig_UnmarshalJSON_AcceptsStringAndNanoseconds(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want time.Duration
	}{
		{"string_form", `{"refresh_interval":"4h"}`, 4 * time.Hour},
		{"ns_integer_form", `{"refresh_interval":14400000000000}`, 4 * time.Hour},
		{"empty", `{"refresh_interval":""}`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c Config
			if err := json.Unmarshal([]byte(tc.raw), &c); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if c.RefreshInterval != tc.want {
				t.Fatalf("RefreshInterval = %v, want %v", c.RefreshInterval, tc.want)
			}
		})
	}
}

func durPtr(d time.Duration) *time.Duration { return &d }
func boolPtr(b bool) *bool                  { return &b }

// TestEffectivePolicy_Inheritance verifies each field resolves independently:
// absent override inherits global; set override wins; explicit false for the
// boolean is a real override, not "absent".
func TestEffectivePolicy_Inheritance(t *testing.T) {
	cfg := Config{
		RefreshInterval:          time.Hour,
		TriggerLeadTime:          5 * time.Minute,
		AutoResetOnWeeklyExhaust: false,
		AccountOverrides: map[string]AccountOverride{
			"a": {TriggerLeadTime: durPtr(10 * time.Minute)},
			"b": {AutoResetOnWeeklyExhaust: boolPtr(true)},
			"c": {AutoResetOnWeeklyExhaust: boolPtr(false)},
		},
	}
	p := cfg.EffectivePolicy("a")
	if p.RefreshInterval != time.Hour || p.TriggerLeadTime != 10*time.Minute || p.AutoResetOnWeeklyExhaust {
		t.Fatalf("a: %+v", p)
	}
	if !cfg.EffectivePolicy("b").AutoResetOnWeeklyExhaust {
		t.Fatal("b must opt in")
	}
	if cfg.EffectivePolicy("c").AutoResetOnWeeklyExhaust {
		t.Fatal("c explicit false must survive")
	}
	// Global flip must not change explicit-false c but must flip inheriting a.
	cfg.AutoResetOnWeeklyExhaust = true
	if cfg.EffectivePolicy("c").AutoResetOnWeeklyExhaust {
		t.Fatal("explicit false must not follow global")
	}
	if !cfg.EffectivePolicy("a").AutoResetOnWeeklyExhaust {
		t.Fatal("inheriting account must follow global")
	}
}

// TestConfig_OverrideRoundTrip verifies sparse overrides survive marshal:
// absent fields stay absent (inherit), explicit false survives, empty
// entries are dropped by normalize.
func TestConfig_OverrideRoundTrip(t *testing.T) {
	cfg := Config{
		RefreshInterval: time.Hour, TriggerLeadTime: 5 * time.Minute,
		AccountOverrides: map[string]AccountOverride{
			"a":     {TriggerLeadTime: durPtr(7 * time.Minute), AutoResetOnWeeklyExhaust: boolPtr(false)},
			"empty": {},
		},
	}
	cfg = normalizeConfig(cfg)
	if _, ok := cfg.AccountOverrides["empty"]; ok {
		t.Fatal("empty override must be dropped")
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Config
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ov := back.AccountOverrides["a"]
	if ov.RefreshInterval != nil {
		t.Fatal("inherited refresh_interval must stay absent")
	}
	if ov.TriggerLeadTime == nil || *ov.TriggerLeadTime != 7*time.Minute {
		t.Fatalf("lead override lost: %+v", ov)
	}
	if ov.AutoResetOnWeeklyExhaust == nil || *ov.AutoResetOnWeeklyExhaust {
		t.Fatal("explicit false must round-trip")
	}
}

// TestLegacyStatePreserved: an old state with 12h/6h and no new fields keeps
// its saved durations (the new defaults apply only to fresh/absent values).
func TestLegacyStatePreserved(t *testing.T) {
	raw := `{"refresh_interval":"12h","trigger_lead_time":"6h","enabled_accounts":["x"]}`
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	cfg = normalizeConfig(cfg)
	if cfg.RefreshInterval != 12*time.Hour || cfg.TriggerLeadTime != 6*time.Hour {
		t.Fatalf("saved legacy durations must be retained: %+v", cfg)
	}
	if cfg.AutoResetOnWeeklyExhaust {
		t.Fatal("missing boolean must default off")
	}
	if cfg.AccountOverrides != nil {
		t.Fatal("missing overrides must stay nil")
	}
}
