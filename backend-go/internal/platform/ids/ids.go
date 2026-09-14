// Package ids provides app-generated identifiers.
//
// Run-domain resources use UUIDv7: time-ordered, generated in the
// application, stored as BINARY(16) in MySQL and rendered as strings by the
// API. SQL stays MySQL-5.7 compatible (no UUID_TO_BIN).
package ids

import (
	"crypto/rand"
	"database/sql/driver"
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"
)

// ID is a UUIDv7 stored as BINARY(16).
type ID [16]byte

// New generates a UUIDv7.
func New() ID {
	u, err := uuid.NewV7()
	if err != nil {
		// uuid.NewV7 only fails if the system RNG is broken.
		var b [16]byte
		_, _ = rand.Read(b[:])
		b[6] = (b[6] & 0x0f) | 0x70
		b[8] = (b[8] & 0x3f) | 0x80
		return ID(b)
	}
	return ID(u)
}

// Parse parses the canonical (or braced/urn) string form.
func Parse(s string) (ID, error) {
	u, err := uuid.Parse(s)
	if err != nil {
		return ID{}, fmt.Errorf("invalid id %q: %w", s, err)
	}
	return ID(u), nil
}

func (id ID) String() string { return uuid.UUID(id).String() }
func (id ID) Bytes() []byte  { return id[:] }

func (id ID) IsZero() bool { return id == ID{} }

// Value implements driver.Valuer.
func (id ID) Value() (driver.Value, error) { return id[:], nil }

// Scan implements sql.Scanner.
func (id *ID) Scan(src any) error {
	switch v := src.(type) {
	case []byte:
		if len(v) == 16 {
			copy(id[:], v)
			return nil
		}
		// Some drivers return the hex text form.
		if len(v) == 36 {
			parsed, err := Parse(string(v))
			if err == nil {
				*id = parsed
				return nil
			}
		}
	case string:
		parsed, err := Parse(v)
		if err == nil {
			*id = parsed
			return nil
		}
	case nil:
		*id = ID{}
		return nil
	}
	return fmt.Errorf("ids: cannot scan %T into ID", src)
}

// MarshalText implements encoding.TextMarshaler (JSON strings).
func (id ID) MarshalText() ([]byte, error) { return []byte(id.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (id *ID) UnmarshalText(text []byte) error {
	parsed, err := Parse(string(text))
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

// Hex renders the raw binary form (debugging).
func (id ID) Hex() string { return hex.EncodeToString(id[:]) }
