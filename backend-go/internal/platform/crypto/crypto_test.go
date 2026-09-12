package crypto

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"golang.org/x/crypto/pbkdf2"
)

func TestAESGCMRoundTrip(t *testing.T) {
	c, err := NewAESGCM("test-secret-key-material")
	if err != nil {
		t.Fatal(err)
	}
	for _, plain := range []string{"", "short", "refresh-token-带中文-77304"} {
		enc, err := c.Encrypt(plain)
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.Decrypt(enc)
		if err != nil {
			t.Fatal(err)
		}
		if got != plain {
			t.Fatalf("round trip = %q, want %q", got, plain)
		}
	}
}

func TestAESGCMUniqueCiphertexts(t *testing.T) {
	c, _ := NewAESGCM("k")
	a, _ := c.Encrypt("same")
	b, _ := c.Encrypt("same")
	if a == b {
		t.Fatal("nonce reuse: identical ciphertexts")
	}
}

func TestHashTokenStableAndSafe(t *testing.T) {
	h1 := HashToken("token-value")
	h2 := HashToken("token-value")
	if h1 != h2 {
		t.Fatal("token hash not deterministic")
	}
	if h1 == "token-value" || len(h1) != 64 {
		t.Fatal("token hash should be sha256 hex, not the raw value")
	}
	if HashToken("other") == h1 {
		t.Fatal("different tokens collide?!")
	}
}

func TestArgon2idRoundTrip(t *testing.T) {
	hash, err := HashPassword("Creator@2026")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := VerifyPassword("Creator@2026", hash)
	if err != nil || !ok {
		t.Fatalf("correct password rejected: ok=%v err=%v", ok, err)
	}
	ok, err = VerifyPassword("wrong", hash)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("wrong password accepted")
	}
}

func TestRandomTokenUniqueness(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		tok, err := RandomToken()
		if err != nil {
			t.Fatal(err)
		}
		if seen[tok] {
			t.Fatal("duplicate random token")
		}
		seen[tok] = true
		if len(tok) != 43 { // 32 bytes base64url
			t.Fatalf("token length %d", len(tok))
		}
	}
}

func TestVerifyDjangoPasswordFormat(t *testing.T) {
	// Build a Django-style hash with a known-good round trip.
	salt := []byte("saltsalt")
	key := pbkdf2.Key([]byte("Creator@2026"), salt, 720000, 32, sha256.New)
	encoded := "pbkdf2_sha256$720000$" + string(salt) + "$" + base64.StdEncoding.EncodeToString(key)
	ok, err := VerifyDjangoPassword("Creator@2026", encoded)
	if err != nil || !ok {
		t.Fatalf("django hash rejected: ok=%v err=%v", ok, err)
	}
	ok, _ = VerifyDjangoPassword("wrong", encoded)
	if ok {
		t.Fatal("django hash accepted wrong password")
	}
	if _, err := VerifyDjangoPassword("x", "$argon2id$v=19$bad"); err == nil {
		t.Fatal("argon format should be rejected by django verifier")
	}
}
