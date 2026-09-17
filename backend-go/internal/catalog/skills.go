package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/creation-agent-studio/backend-go/internal/platform/dbtypes"
)

// Skill is an AGENT-SCOPED skill: a named prompt fragment the composer
// prepends to the user's message before the Run is created.
//
// It is deliberately NOT a provider capability. The provider (Aily or
// otherwise) never sees a "skill" — it sees an ordinary message whose text
// happens to start with the fragment, and interpreting that fragment is the
// custom agent's own business. That keeps the whole feature inside the
// catalog + composer, with no Run-pipeline, idempotency-hash or adapter
// change: the same skill produces the same `content`, so the existing
// request hash already covers it.
type Skill struct {
	// ID is stable across renames so a saved composer selection survives an
	// admin editing the skill's wording. Derived from the name when the
	// client does not supply one.
	ID string `json:"id"`
	// Name is the composer chip label (「查询收入数据」).
	Name string `json:"name"`
	// Description is the one-line subtitle in the skill sheet.
	Description string `json:"description"`
	// Prompt is prepended verbatim to the user's message (「/查询收入查询」).
	Prompt string `json:"prompt"`
}

const (
	// MaxSkillsPerApplication bounds the sheet and the stored payload. The
	// design report caps the composer badge at "技能 9+" (§9.2), so a
	// ceiling well above that is pointless.
	MaxSkillsPerApplication = 20
	maxSkillNameRunes       = 24
	maxSkillDescRunes       = 60
	maxSkillPromptRunes     = 200
	maxSkillIDLen           = 64
)

// ErrInvalidSkill marks a skill payload the catalog refuses. The transport
// layer maps it to 400 with the field name, never to a 500.
var ErrInvalidSkill = errors.New("技能配置不合法")

var skillIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// NormalizeSkills trims, validates and de-duplicates a submitted skill list.
//
// It is a PURE function of its input (no DB, no clock) so the rules can be
// proven directly, and it never silently drops a malformed entry: an admin
// who typed a skill with no prompt must be told, not quietly saved without it.
// The ONE thing it does repair is a missing id, which is derived from the
// name — an absent id is a client convenience, not a user mistake.
func NormalizeSkills(in []Skill) ([]Skill, error) {
	if len(in) > MaxSkillsPerApplication {
		return nil, fmt.Errorf("%w：单次最多 %d 个技能", ErrInvalidSkill, MaxSkillsPerApplication)
	}
	out := make([]Skill, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for i, raw := range in {
		name := strings.TrimSpace(raw.Name)
		if name == "" {
			return nil, fmt.Errorf("%w：第 %d 个技能缺少名称", ErrInvalidSkill, i+1)
		}
		if len([]rune(name)) > maxSkillNameRunes {
			return nil, fmt.Errorf("%w：技能名称不能超过 %d 个字", ErrInvalidSkill, maxSkillNameRunes)
		}
		prompt := strings.TrimSpace(raw.Prompt)
		if prompt == "" {
			return nil, fmt.Errorf("%w：「%s」缺少提示词", ErrInvalidSkill, name)
		}
		if len([]rune(prompt)) > maxSkillPromptRunes {
			return nil, fmt.Errorf("%w：「%s」的提示词不能超过 %d 个字", ErrInvalidSkill, name, maxSkillPromptRunes)
		}
		desc := strings.TrimSpace(raw.Description)
		if len([]rune(desc)) > maxSkillDescRunes {
			return nil, fmt.Errorf("%w：「%s」的说明不能超过 %d 个字", ErrInvalidSkill, name, maxSkillDescRunes)
		}

		id := strings.TrimSpace(raw.ID)
		if id == "" {
			id = slugifySkillName(name)
		}
		if len(id) > maxSkillIDLen {
			return nil, fmt.Errorf("%w：「%s」的标识过长", ErrInvalidSkill, name)
		}
		if !skillIDPattern.MatchString(id) {
			return nil, fmt.Errorf("%w：「%s」的标识仅支持小写字母、数字和连字符", ErrInvalidSkill, name)
		}
		// A duplicate id would collapse two chips into one selection, so it
		// is a rejection rather than a silent merge.
		if _, dup := seen[id]; dup {
			return nil, fmt.Errorf("%w：技能标识「%s」重复", ErrInvalidSkill, id)
		}
		seen[id] = struct{}{}
		out = append(out, Skill{ID: id, Name: name, Description: desc, Prompt: prompt})
	}
	return out, nil
}

// slugifySkillName derives a stable, pattern-valid id from a skill name.
// Non-ASCII names (the common case here — 查询收入数据) have no ASCII form, so
// the fallback is a deterministic hash of the name: still stable across
// saves, and never collides with a differently-named skill.
func slugifySkillName(name string) string {
	lower := strings.ToLower(name)
	var b strings.Builder
	lastDash := false
	for _, r := range lower {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_' || r == ' ':
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" || skillIDPattern.MatchString(slug) == false {
		return fmt.Sprintf("skill-%x", fnv32(name))
	}
	return slug
}

func fnv32(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// ParseSkills reads the `skills` array out of an application's default_config.
//
// The result is ALWAYS non-nil: the wire payloads must carry `[]` rather than
// `null` for an agent with no skills, so the mobile sheet can tell "the
// catalog says none" apart from "this field never arrived".
//
// A malformed payload yields an empty list rather than an error: this runs on
// every catalog read, and one bad row must not blank out the whole mobile
// selector. Unknown keys inside an entry are ignored, matching the JSON
// column's schemaless nature.
func ParseSkills(raw dbtypes.JSONText) []Skill {
	out := []Skill{}
	if len(raw) == 0 {
		return out
	}
	var doc struct {
		Skills []Skill `json:"skills"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return out
	}
	for _, s := range doc.Skills {
		if strings.TrimSpace(s.Name) == "" || strings.TrimSpace(s.Prompt) == "" {
			continue
		}
		out = append(out, Skill{
			ID:          strings.TrimSpace(s.ID),
			Name:        strings.TrimSpace(s.Name),
			Description: strings.TrimSpace(s.Description),
			Prompt:      strings.TrimSpace(s.Prompt),
		})
	}
	return out
}

// MergeSkills writes the skills array back into a default_config document,
// PRESERVING every other key.
//
// This is why it is a read-modify-write rather than a plain overwrite: the
// column is also where a legacy editor parked `guided_entry_prompt_key`, and
// saving 技能配置 must not take that with it. An empty list REMOVES the key
// instead of storing `"skills": null`, so a cleared agent and a never-
// configured agent are byte-identical.
func MergeSkills(raw dbtypes.JSONText, skills []Skill) (dbtypes.JSONText, error) {
	doc := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &doc); err != nil {
			// A corrupt document cannot be merged into safely. Refuse rather
			// than replace it — silently dropping unknown keys is worse than
			// an admin retrying.
			return nil, fmt.Errorf("%w：default_config 不是合法的 JSON 对象", ErrInvalidSkill)
		}
	}
	if len(skills) == 0 {
		delete(doc, "skills")
	} else {
		doc["skills"] = skills
	}
	if len(doc) == 0 {
		// Nothing left to store; keep the column NULL rather than "{}".
		return nil, nil
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("%w：技能配置序列化失败", ErrInvalidSkill)
	}
	return dbtypes.JSONText(encoded), nil
}
