// Package dbtypes provides database/sql compatible types for column
// kinds that the stdlib conversion table mishandles.
//
// Why not json.RawMessage: under Go's encoding/json v2 runtime the type
// resolves to jsontext.Value, and database/sql's nil fast-path (a type
// switch on *[]byte) no longer matches it — scanning a SQL NULL then
// fails. JSONText implements Scanner/Valuer explicitly, which works under
// both the v1 and v2 runtimes.
package dbtypes

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
)

// JSONText is a nullable JSON column: NULL scans to nil, and an empty
// value writes NULL.
type JSONText []byte

// Scan implements sql.Scanner.
func (j *JSONText) Scan(src any) error {
	if src == nil {
		*j = nil
		return nil
	}
	switch v := src.(type) {
	case []byte:
		out := make(JSONText, len(v))
		copy(out, v)
		*j = out
		return nil
	case string:
		*j = JSONText(v)
		return nil
	default:
		return fmt.Errorf("dbtypes: cannot scan %T into JSONText", src)
	}
}

// Value implements driver.Valuer.
func (j JSONText) Value() (driver.Value, error) {
	if len(j) == 0 {
		return nil, nil
	}
	return string(j), nil
}

// IsNull reports whether the value holds SQL NULL / nothing.
func (j JSONText) IsNull() bool { return len(j) == 0 }

// Unmarshal decodes the JSON into out; a NULL column is a no-op.
func (j JSONText) Unmarshal(out any) error {
	if len(j) == 0 {
		return nil
	}
	return jsonUnmarshalInto([]byte(j), out)
}

// Marshal encodes v as JSON; nil encodes to NULL.
func MarshalJSON(v any) (JSONText, error) {
	if v == nil {
		return nil, nil
	}
	raw, err := jsonMarshal(v)
	if err != nil {
		return nil, err
	}
	return JSONText(raw), nil
}

var errNotJSON = errors.New("dbtypes: invalid json")

func init() { _ = sql.ErrNoRows }
