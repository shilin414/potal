package catalog

// Review 12 (2026-09-17, 执行报告 §21 / P2-3): the paged catalog's cursor is
// opaque but NOT trusted. `{}` decodes cleanly into the zero PageCursor, and
// the zero value is exactly what "start at page one" means internally — so a
// hand-crafted cursor would silently restart the walk instead of failing.
// These tests are DB-free: the guard lives in DecodePageCursor.

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

func TestDecodePageCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 9, 17, 10, 30, 0, 0, time.UTC)
	raw := EncodePageCursor(at, 42)
	gotAt, gotID, err := DecodePageCursor(raw)
	if err != nil {
		t.Fatalf("round trip failed: %v", err)
	}
	if !gotAt.Equal(at) || gotID != 42 {
		t.Fatalf("round trip changed the anchor: %v / %d", gotAt, gotID)
	}
}

func TestDecodePageCursorRejectsMalformedAndEmptyAnchors(t *testing.T) {
	encode := func(payload string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(payload))
	}
	cases := map[string]string{
		"not base64":     "!!!not-base64!!!",
		"not json":       encode("not-json"),
		"empty object":   encode(`{}`),
		"zero anchor":    encode(`{"c":"0001-01-01T00:00:00Z","i":0}`),
		"zero id":        encode(`{"c":"2026-09-17T10:30:00Z","i":0}`),
		"negative id":    encode(`{"c":"2026-09-17T10:30:00Z","i":-7}`),
		"zero timestamp": encode(`{"c":"0001-01-01T00:00:00Z","i":5}`),
		"empty string":   "",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := DecodePageCursor(raw); err == nil {
				t.Fatalf("cursor %q (%s) must be rejected", raw, name)
			}
		})
	}

	// The sentinel is exported so handlers/tests can assert the cause rather
	// than merely "some error".
	_, _, err := DecodePageCursor(encode(`{}`))
	if !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("an empty anchor must fail with ErrInvalidCursor, got %v", err)
	}
}
