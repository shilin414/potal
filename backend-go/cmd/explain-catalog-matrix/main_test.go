package main

import (
	"strings"
	"testing"
)

func TestValidateSyntheticConfigRejectsBindingTruncation(t *testing.T) {
	err := validateSyntheticConfig(syntheticConfig{
		Applications: 5,
		Bindings:     6,
		RunsPerUser:  10,
		Users:        1,
	})
	if err == nil || !strings.Contains(err.Error(), "exceed applications") {
		t.Fatalf("expected an explicit binding/application mismatch, got %v", err)
	}
}

func TestSyntheticFixtureReportsRequestedCountsExactly(t *testing.T) {
	fixture := newSyntheticFixture(syntheticConfig{
		Applications: 5000,
		Bindings:     4000,
		RunsPerUser:  100000,
		Users:        2,
	})
	if fixture.Applications != 5000 || fixture.Bindings != 4000 ||
		fixture.Runs != 200000 || fixture.Users != 2 {
		t.Fatalf("fixture counts drifted: %+v", fixture)
	}
	if !strings.HasPrefix(fixture.Prefix, syntheticPrefixBase) {
		t.Fatalf("fixture prefix %q must start with %q", fixture.Prefix, syntheticPrefixBase)
	}
}

func TestSyntheticCleanupUsesLiteralPrefixComparison(t *testing.T) {
	for name, query := range map[string]string{
		"runs":         cleanupRunsByPrefixSQL,
		"applications": cleanupApplicationsByPrefixSQL,
	} {
		if !strings.Contains(query, "LEFT(slug, CHAR_LENGTH(?)) = ?") {
			t.Fatalf("%s cleanup must compare the exact prefix: %s", name, query)
		}
		if strings.Contains(strings.ToUpper(query), " LIKE ") {
			t.Fatalf("%s cleanup must not use LIKE because '_' is a wildcard: %s", name, query)
		}
	}
}

func TestRunTimedBenchmarkExecutesEveryIteration(t *testing.T) {
	calls := 0
	if err := runTimedBenchmark("unit", 3, func() error {
		calls++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("calls=%d, want 3", calls)
	}
}
