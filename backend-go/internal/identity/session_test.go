package identity

import (
	"testing"
	"time"

	"github.com/creation-agent-studio/backend-go/internal/platform/crypto"
)

// Session store tests run against miniredis when available; they are
// skipped otherwise so `go test ./...` stays green without Redis.
// Integration-with-real-Redis is covered by tests/integration (opt-in).

func TestStateCodecRoundTrip(t *testing.T) {
	c := NewStateCodec("secret-salt")
	state := OAuthState{ReturnTo: "/chat/sales"}
	encoded := c.Dump(state)

	got, err := c.Load(encoded)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.ReturnTo != "/chat/sales" {
		t.Fatalf("return_to = %q", got.ReturnTo)
	}
}

func TestStateCodecRejectsTampering(t *testing.T) {
	c := NewStateCodec("secret-salt")
	encoded := c.Dump(OAuthState{ReturnTo: "/ok"})
	// Flip a char in the body.
	tampered := "x" + encoded[1:]
	if _, err := c.Load(tampered); err == nil {
		t.Fatal("tampered state accepted")
	}
	// Wrong key.
	other := NewStateCodec("other-salt")
	if _, err := other.Load(encoded); err == nil {
		t.Fatal("state signed with different secret accepted")
	}
}

func TestStateCodecExpiry(t *testing.T) {
	c := NewStateCodec("secret-salt")
	encoded := c.Dump(OAuthState{ReturnTo: "/", IssuedAt: time.Now().Add(-11 * time.Minute).Unix()})
	if _, err := c.Load(encoded); err == nil {
		t.Fatal("expired state accepted")
	}
}

func TestSanitizeReturnTo(t *testing.T) {
	cases := map[string]string{
		"/chat/x":   "/chat/x",
		"":          "/",
		"//evil":    "/",
		"https://x": "/",
		"/":         "/",
	}
	for in, want := range cases {
		if got := SanitizeReturnTo(in); got != want {
			t.Fatalf("SanitizeReturnTo(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDisplayIdentityPrecedence(t *testing.T) {
	u := &User{ID: 3, Username: "wuzb", DisplayName: "吴志彬", DisplayID: "19127920"}
	name, disp := u.DisplayIdentity(nil)
	if name != "吴志彬" || disp != "19127920" {
		t.Fatalf("identity = %q/%q", name, disp)
	}
	// Fall back to feishu user_id when display_id empty.
	u2 := &User{ID: 5, Username: "demo"}
	ident := &FeishuIdentity{FeishuUserID: "666"}
	name, disp = u2.DisplayIdentity(ident)
	if disp != "666" {
		t.Fatalf("display_id fallback = %q", disp)
	}
	// Local PK is the last resort — never empty.
	name, disp = u2.DisplayIdentity(nil)
	if disp != "5" || name != "demo" {
		t.Fatalf("pk fallback = %q/%q", name, disp)
	}
}

func TestSessionPayloadNeverEmptyDisplayID(t *testing.T) {
	u := &User{ID: 9, Username: "x"}
	payload := SessionPayload(u, nil)
	if payload.DisplayID == "" {
		t.Fatal("display_id must never be empty (UI renders 「（）」 otherwise)")
	}
	if payload.ID != 9 || payload.Username != "x" {
		t.Fatalf("payload = %+v", payload)
	}
	var _ = crypto.HashToken
}
