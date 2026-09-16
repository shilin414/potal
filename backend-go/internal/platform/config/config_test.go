package config

import (
	"reflect"
	"testing"
	"time"
)

// TestParseWeights requires a strictly positive share for every priority
// class: interactive must always lead and scheduled/retry must never starve
// (weight 0 would silently disable a class), so every invalid shape falls
// back to the safe 7/1/2 default.
func TestParseWeights(t *testing.T) {
	cases := []struct {
		in   string
		want []int
	}{
		{"7,1,2", []int{7, 1, 2}},
		{" 7 , 1 , 2 ", []int{7, 1, 2}},
		{"10,3,4", []int{10, 3, 4}},
		{"1,1,1", []int{1, 1, 1}},
		// Zero weights are rejected: a class must keep a non-zero share.
		{"0,1,2", []int{7, 1, 2}},
		{"7,0,2", []int{7, 1, 2}},
		{"7,1,0", []int{7, 1, 2}},
		{"0,0,0", []int{7, 1, 2}},
		// Negative / non-numeric / wrong arity are rejected too.
		{"-1,1,2", []int{7, 1, 2}},
		{"7,x,2", []int{7, 1, 2}},
		{"7,1", []int{7, 1, 2}},
		{"7,1,2,3", []int{7, 1, 2}},
		{"", []int{7, 1, 2}},
	}
	for _, c := range cases {
		if got := parseWeights(c.in); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("parseWeights(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// sseEnvKeys are the Batch 4 hub bounds. Tests neutralize them by setting an
// EMPTY value rather than unsetting them: getEnv* treats "" as absent, and an
// empty-but-present variable also stops loadDotEnv from filling it in from a
// developer's .env.local.
var sseEnvKeys = []string{
	"SSE_HUB_CACHE_EVENTS",
	"SSE_HUB_CACHE_BYTES",
	"SSE_SUBSCRIBER_QUEUE_EVENTS",
	"SSE_SUBSCRIBER_QUEUE_BYTES",
	"SSE_HUB_IDLE_TTL",
}

func neutralizeSSEEnv(t *testing.T) {
	t.Helper()
	for _, key := range sseEnvKeys {
		t.Setenv(key, "")
	}
}

func defaultSSEConfig() SSEConfig {
	return SSEConfig{
		HubCacheEvents:   2048,
		HubCacheBytes:    8 << 20,
		SubscriberEvents: 1024,
		SubscriberBytes:  4 << 20,
		HubIdleTTL:       30 * time.Second,
	}
}

// TestSSEConfigDefaults pins the documented hub bounds. They are the memory
// budget of a long-lived process, so an accidental default change is a
// production incident, not a preference.
func TestSSEConfigDefaults(t *testing.T) {
	neutralizeSSEEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SSE != defaultSSEConfig() {
		t.Fatalf("SSE defaults = %+v, want %+v", cfg.SSE, defaultSSEConfig())
	}
}

func TestSSEConfigEnvOverrides(t *testing.T) {
	t.Setenv("SSE_HUB_CACHE_EVENTS", "16")
	t.Setenv("SSE_HUB_CACHE_BYTES", "1048576")
	t.Setenv("SSE_SUBSCRIBER_QUEUE_EVENTS", "8")
	t.Setenv("SSE_SUBSCRIBER_QUEUE_BYTES", "2048")
	t.Setenv("SSE_HUB_IDLE_TTL", "5s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := SSEConfig{
		HubCacheEvents:   16,
		HubCacheBytes:    1 << 20,
		SubscriberEvents: 8,
		SubscriberBytes:  2048,
		HubIdleTTL:       5 * time.Second,
	}
	if cfg.SSE != want {
		t.Fatalf("SSE from env = %+v, want %+v", cfg.SSE, want)
	}
}

// TestSSEConfigRejectsUnusableValues is the one that matters for safety.
//
// These fields are MEMORY BOUNDS. A zero or negative value must therefore never
// reach the hub as "unlimited" — it falls back to the documented default
// exactly like unparsable junk does, so a typo (or a well-meant
// SSE_HUB_CACHE_EVENTS=0 meant as "off") cannot produce an unbounded cache in
// a long-running process.
func TestSSEConfigRejectsUnusableValues(t *testing.T) {
	cases := []struct {
		name   string
		values []string
	}{
		{"zero", []string{"0", "0", "0", "0", "0"}},
		{"negative", []string{"-1", "-4096", "-2", "-1", "-30s"}},
		{"junk", []string{"abc", "many", "lots", "big", "soon"}},
		{"empty", []string{"", "", "", "", ""}},
		{"mixed", []string{"abc", "0", "-5", "", "junk"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for i, key := range sseEnvKeys {
				t.Setenv(key, tc.values[i])
			}
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.SSE != defaultSSEConfig() {
				t.Fatalf("SSE with %s values = %+v, want the defaults %+v "+
					"(a bound must never degrade to \"unlimited\")", tc.name, cfg.SSE, defaultSSEConfig())
			}
		})
	}

	t.Run("bare seconds accepted", func(t *testing.T) {
		neutralizeSSEEnv(t)
		t.Setenv("SSE_HUB_IDLE_TTL", "45")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.SSE.HubIdleTTL != 45*time.Second {
			t.Fatalf("HubIdleTTL = %s, want 45s", cfg.SSE.HubIdleTTL)
		}
	})
}

// TestEnvPositiveLoadersRejectNonPositive exercises the loaders directly, so a
// failure points at the parser rather than at Load.
func TestEnvPositiveLoadersRejectNonPositive(t *testing.T) {
	t.Run("int", func(t *testing.T) {
		for _, raw := range []string{"0", "-1", "x", ""} {
			t.Setenv("TEST_POSITIVE_INT", raw)
			if got := getEnvPositiveInt("TEST_POSITIVE_INT", 7); got != 7 {
				t.Fatalf("getEnvPositiveInt(%q) = %d, want 7", raw, got)
			}
		}
		t.Setenv("TEST_POSITIVE_INT", "9")
		if got := getEnvPositiveInt("TEST_POSITIVE_INT", 7); got != 9 {
			t.Fatalf("getEnvPositiveInt = %d, want 9", got)
		}
	})

	t.Run("int64", func(t *testing.T) {
		for _, raw := range []string{"0", "-1", "1.5", "x", ""} {
			t.Setenv("TEST_POSITIVE_INT64", raw)
			if got := getEnvPositiveInt64("TEST_POSITIVE_INT64", 7); got != 7 {
				t.Fatalf("getEnvPositiveInt64(%q) = %d, want 7", raw, got)
			}
		}
		t.Setenv("TEST_POSITIVE_INT64", "8388608")
		if got := getEnvPositiveInt64("TEST_POSITIVE_INT64", 7); got != 8388608 {
			t.Fatalf("getEnvPositiveInt64 = %d, want 8388608", got)
		}
	})

	t.Run("duration", func(t *testing.T) {
		for _, raw := range []string{"0", "-1s", "0s", "x", ""} {
			t.Setenv("TEST_POSITIVE_DURATION", raw)
			if got := getEnvPositiveDuration("TEST_POSITIVE_DURATION", 7*time.Second); got != 7*time.Second {
				t.Fatalf("getEnvPositiveDuration(%q) = %s, want 7s", raw, got)
			}
		}
		t.Setenv("TEST_POSITIVE_DURATION", "1.5s")
		if got := getEnvPositiveDuration("TEST_POSITIVE_DURATION", 7*time.Second); got != 1500*time.Millisecond {
			t.Fatalf("getEnvPositiveDuration = %s, want 1.5s", got)
		}
	})
}
