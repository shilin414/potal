// Package crypto provides the security primitives:
//
//   - AES-256-GCM encryption for Feishu refresh tokens at rest
//   - Argon2id password hashing for local admin accounts
//   - CSPRNG session tokens
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/pbkdf2"
)

// ────────────────────────────────────────────────── random tokens ──

// RandomToken returns a 256-bit cryptographically random token,
// base64url encoded (no padding) — suitable for session cookies.
func RandomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashToken hashes a session token for storage (we keep the hash, not the
// raw value, in Redis).
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ConstantTimeEqual compares two strings in constant time.
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ────────────────────────────────────────────────── AES-256-GCM ──

type AESGCM struct {
	aead cipher.AEAD
}

// NewAESGCM derives a 256-bit key from the configured secret with SHA-256
// (the key never leaves memory; a raw 32-byte base64 key is also accepted).
func NewAESGCM(secret string) (*AESGCM, error) {
	if secret == "" {
		return nil, errors.New("crypto: empty encryption secret")
	}
	var key []byte
	if raw, err := base64.StdEncoding.DecodeString(secret); err == nil && len(raw) == 32 {
		key = raw
	} else {
		sum := sha256.Sum256([]byte(secret))
		key = sum[:]
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &AESGCM{aead: aead}, nil
}

// Encrypt returns nonce||ciphertext, base64 (std, padded).
func (c *AESGCM) Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := c.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(out), nil
}

// Decrypt reverses Encrypt. Empty input yields an empty string.
func (c *AESGCM) Decrypt(ciphertext string) (string, error) {
	if ciphertext == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", fmt.Errorf("crypto: decode: %w", err)
	}
	if len(raw) < c.aead.NonceSize() {
		return "", errors.New("crypto: ciphertext too short")
	}
	nonce, body := raw[:c.aead.NonceSize()], raw[c.aead.NonceSize():]
	plaintext, err := c.aead.Open(nil, nonce, body, nil)
	if err != nil {
		return "", fmt.Errorf("crypto: open: %w", err)
	}
	return string(plaintext), nil
}

// ─────────────────────────────────────────────────────── Argon2id ──

const (
	argonTime    = 1
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

// HashPassword encodes an Argon2id hash:
// $argon2id$v=19$m=65536,t=2,p=2$<salt-b64>$<hash-b64>
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword checks a password against an Argon2id (PHC-format)
// encoded hash:
//
//	$argon2id$v=19$m=65536,t=2,p=2$<salt-b64>$<hash-b64>
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	// ["", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("crypto: unsupported hash format")
	}
	var version uint32
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, fmt.Errorf("crypto: bad version: %w", err)
	}
	var (
		memory uint32
		timeC  uint32
		par    uint8
	)
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &timeC, &par); err != nil {
		return false, fmt.Errorf("crypto: bad hash params: %w", err)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("crypto: bad salt: %w", err)
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("crypto: bad hash: %w", err)
	}
	got := argon2.IDKey([]byte(password), salt, timeC, memory, par, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// VerifyDjangoPassword verifies legacy Django password hashes:
//
//	pbkdf2_sha256$<iterations>$<salt>$<hash-b64>
//
// Per Django's PBKDF2PasswordHasher the SALT is a raw string (NOT base64);
// only the derived hash is base64. Kept for the xiaoan3 → xiaoan3_go data
// migration so existing local accounts keep working; NEW passwords are
// always Argon2id.
func VerifyDjangoPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2_sha256" {
		return false, errors.New("crypto: unsupported django hash format")
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations <= 0 {
		return false, errors.New("crypto: bad django iterations")
	}
	salt := []byte(parts[2]) // raw string, per Django's encode()
	want, err := base64.StdEncoding.DecodeString(parts[3])
	if err != nil {
		return false, fmt.Errorf("crypto: bad django hash: %w", err)
	}
	got := pbkdf2.Key([]byte(password), salt, iterations, len(want), sha256.New)
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
