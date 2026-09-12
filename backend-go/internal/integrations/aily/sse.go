package aily

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
)

// sseReader reads Server-Sent Events line by line with context
// cancellation. Next returns (line, ok=true) per line; ok=false on EOF.
type sseReader struct {
	scanner *bufio.Scanner
	closer  io.Closer
}

func newSSEReader(r io.Reader) *sseReader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // SSE frames can be large
	return &sseReader{scanner: sc}
}

// Next blocks for the next line. Errors from the transport surface here;
// context cancellation stops a blocked read.
func (s *sseReader) Next(ctx context.Context) (string, bool, error) {
	type result struct {
		line string
		ok   bool
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, ok := "", false
		var err error
		if s.scanner.Scan() {
			line = s.scanner.Text()
			ok = true
		} else {
			err = s.scanner.Err()
		}
		ch <- result{line, ok, err}
	}()
	select {
	case <-ctx.Done():
		return "", false, ctx.Err()
	case r := <-ch:
		if r.err != nil && errors.Is(r.err, io.EOF) {
			return "", false, nil
		}
		if r.err != nil {
			// Surface unexpected transport errors (including 5xx style
			// body truncation) as transport failures for reconciliation.
			return "", false, &httpError{err: r.err}
		}
		return r.line, r.ok, nil
	}
}

type httpError struct{ err error }

func (e *httpError) Error() string { return "aily: stream transport error: " + e.err.Error() }
func (e *httpError) Unwrap() error { return e.err }

var _ = http.StatusOK
