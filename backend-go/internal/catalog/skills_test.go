package catalog

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/creation-agent-studio/backend-go/internal/platform/dbtypes"
)

// A skill with no id gets a DERIVED one, and deriving it twice must give the
// same answer: the composer persists the id in the user's selection, so an
// unstable derivation would silently drop the selection on every reload.
func TestNormalizeSkillsDerivesStableID(t *testing.T) {
	first, err := NormalizeSkills([]Skill{{Name: "查询收入数据", Prompt: "/查询收入查询"}})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	second, err := NormalizeSkills([]Skill{{Name: "查询收入数据", Prompt: "/查询收入查询"}})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if first[0].ID == "" {
		t.Fatal("expected a derived id, got empty")
	}
	if first[0].ID != second[0].ID {
		t.Fatalf("id derivation is not stable: %q vs %q", first[0].ID, second[0].ID)
	}
	if !skillIDPattern.MatchString(first[0].ID) {
		t.Fatalf("derived id %q violates the pattern", first[0].ID)
	}
}

// A client-supplied id is passed through VERBATIM when valid, and REJECTED
// when malformed — it is never silently normalized. Same convention as
// application slugs (ErrBadSlug): a quietly rewritten identifier would turn a
// typo into a different, still-working skill, and two typos into a collision
// the admin never sees.
func TestNormalizeSkillsTreatsIDsStrictly(t *testing.T) {
	out, err := NormalizeSkills([]Skill{{ID: "data-analysis", Name: "数据分析", Prompt: "/分析"}})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if out[0].ID != "data-analysis" {
		t.Fatalf("valid id was rewritten: %q", out[0].ID)
	}

	for _, bad := range []string{"Data-Analysis", "data_analysis", "数据分析", "data analysis", "-lead", "trail-"} {
		if _, err := NormalizeSkills([]Skill{{ID: bad, Name: "数据分析", Prompt: "/分析"}}); !errors.Is(err, ErrInvalidSkill) {
			t.Fatalf("id %q must be rejected, got %v", bad, err)
		}
	}
}

func TestNormalizeSkillsTrimsAndPreservesPromptVerbatim(t *testing.T) {
	out, err := NormalizeSkills([]Skill{{
		Name: "  查询收入数据  ", Description: "  按月查询  ", Prompt: "  /查询收入查询  ",
	}})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if out[0].Name != "查询收入数据" || out[0].Description != "按月查询" {
		t.Fatalf("expected trimmed fields, got %+v", out[0])
	}
	// The prompt is prepended to the user's message, so only the OUTER
	// whitespace is stripped — interior spacing is the admin's text.
	if out[0].Prompt != "/查询收入查询" {
		t.Fatalf("unexpected prompt %q", out[0].Prompt)
	}
}

func TestNormalizeSkillsRejectsIncompleteEntries(t *testing.T) {
	cases := map[string][]Skill{
		"no name":     {{Prompt: "/x"}},
		"blank name":  {{Name: "   ", Prompt: "/x"}},
		"no prompt":   {{Name: "查询收入数据"}},
		"blank promt": {{Name: "查询收入数据", Prompt: "  "}},
	}
	for label, in := range cases {
		if _, err := NormalizeSkills(in); !errors.Is(err, ErrInvalidSkill) {
			t.Fatalf("%s: expected ErrInvalidSkill, got %v", label, err)
		}
	}
}

func TestNormalizeSkillsRejectsDuplicatesAndOverlong(t *testing.T) {
	if _, err := NormalizeSkills([]Skill{
		{ID: "dup", Name: "A", Prompt: "/a"},
		{ID: "dup", Name: "B", Prompt: "/b"},
	}); !errors.Is(err, ErrInvalidSkill) {
		t.Fatalf("duplicate id must be rejected, got %v", err)
	}

	if _, err := NormalizeSkills([]Skill{
		{Name: strings.Repeat("字", maxSkillNameRunes+1), Prompt: "/a"},
	}); !errors.Is(err, ErrInvalidSkill) {
		t.Fatalf("overlong name must be rejected, got %v", err)
	}

	tooMany := make([]Skill, MaxSkillsPerApplication+1)
	for i := range tooMany {
		tooMany[i] = Skill{Name: "s", Prompt: "/p"}
	}
	if _, err := NormalizeSkills(tooMany); !errors.Is(err, ErrInvalidSkill) {
		t.Fatalf("too many skills must be rejected, got %v", err)
	}
}

func TestParseSkillsAlwaysReturnsNonNil(t *testing.T) {
	for label, raw := range map[string]dbtypes.JSONText{
		"nil":         nil,
		"empty":       dbtypes.JSONText(""),
		"no skills":   dbtypes.JSONText(`{"guided_entry_prompt_key":"x"}`),
		"bad json":    dbtypes.JSONText(`{not json`),
		"empty array": dbtypes.JSONText(`{"skills":[]}`),
	} {
		got := ParseSkills(raw)
		if got == nil {
			t.Fatalf("%s: ParseSkills returned nil; the wire payload would be null, not []", label)
		}
		if len(got) != 0 {
			t.Fatalf("%s: expected no skills, got %+v", label, got)
		}
	}
}

func TestParseSkillsSkipsIncompleteStoredEntries(t *testing.T) {
	got := ParseSkills(dbtypes.JSONText(
		`{"skills":[{"id":"a","name":"数据分析","prompt":"/分析"},{"name":"缺提示词"}]}`))
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("expected only the complete entry, got %+v", got)
	}
}

// The load-bearing property: saving 技能配置 must not destroy the other keys
// a legacy editor stored in the SAME JSON column.
func TestMergeSkillsPreservesOtherDefaultConfigKeys(t *testing.T) {
	raw := dbtypes.JSONText(`{"guided_entry_prompt_key":"welcome","other":123}`)

	merged, err := MergeSkills(raw, []Skill{{ID: "a", Name: "A", Prompt: "/a"}})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(merged, &doc); err != nil {
		t.Fatalf("merged document is not valid JSON: %v", err)
	}
	if doc["guided_entry_prompt_key"] != "welcome" {
		t.Fatalf("guided_entry_prompt_key was dropped: %+v", doc)
	}
	if doc["other"] != float64(123) {
		t.Fatalf("unrelated key was dropped: %+v", doc)
	}
	if _, ok := doc["skills"]; !ok {
		t.Fatalf("skills missing from the merged document: %+v", doc)
	}
}

// Clearing skills must REMOVE the key, so a cleared agent and a never-
// configured agent are stored identically.
func TestMergeSkillsEmptyRemovesTheKey(t *testing.T) {
	raw := dbtypes.JSONText(`{"skills":[{"id":"a","name":"A","prompt":"/a"}],"keep":"yes"}`)

	merged, err := MergeSkills(raw, nil)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(merged, &doc); err != nil {
		t.Fatalf("merged document is not valid JSON: %v", err)
	}
	if _, ok := doc["skills"]; ok {
		t.Fatalf("skills key should have been removed: %+v", doc)
	}
	if doc["keep"] != "yes" {
		t.Fatalf("unrelated key was dropped: %+v", doc)
	}
}

func TestMergeSkillsEmptyDocumentBecomesNull(t *testing.T) {
	merged, err := MergeSkills(nil, nil)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	// NULL, not "{}": an empty document would make ParseSkills and every
	// legacy reader see a non-null config that means nothing.
	if merged != nil {
		t.Fatalf("expected a NULL column, got %q", string(merged))
	}
}

// A corrupt document cannot be merged into safely; refusing beats silently
// discarding whatever keys it held.
func TestMergeSkillsRefusesCorruptDocument(t *testing.T) {
	if _, err := MergeSkills(dbtypes.JSONText(`[1,2,3]`), []Skill{{Name: "A", Prompt: "/a"}}); err == nil {
		t.Fatal("expected an error for a non-object default_config")
	}
}

// Round trip: what MergeSkills writes, ParseSkills reads back unchanged.
func TestSkillsRoundTrip(t *testing.T) {
	in := []Skill{
		{ID: "income", Name: "查询收入数据", Description: "按月查询", Prompt: "/查询收入查询"},
		{ID: "chart", Name: "图表分析", Prompt: "/图表分析"},
	}

	merged, err := MergeSkills(nil, in)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	got := ParseSkills(merged)

	if len(got) != len(in) {
		t.Fatalf("round trip changed the length: %d → %d", len(in), len(got))
	}
	for i := range in {
		if got[i] != in[i] {
			t.Fatalf("entry %d changed: %+v → %+v", i, in[i], got[i])
		}
	}
}

// The derived id must satisfy the pattern check NormalizeSkills enforces —
// otherwise an id-less skill could be saved once and rejected on the next edit.
func TestDerivedIDPassesValidation(t *testing.T) {
	out, err := NormalizeSkills([]Skill{{Name: "查询收入数据", Prompt: "/查询收入查询"}})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	again, err := NormalizeSkills(out)
	if err != nil {
		t.Fatalf("re-normalizing a normalized list must succeed: %v", err)
	}
	if again[0].ID != out[0].ID {
		t.Fatalf("id not preserved across save: %q → %q", out[0].ID, again[0].ID)
	}
}
