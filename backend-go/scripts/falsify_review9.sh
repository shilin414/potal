#!/usr/bin/env bash
# Falsification driver (第九轮): revert one fix at a time, prove the matching
# test FAILS, restore, prove it passes again. A test that cannot fail proves
# nothing.
set -u
export PATH="/usr/bin:/bin:/c/software/Git/cmd:$PATH"
cd "$(dirname "$0")/.." || exit 1
ROOT="$(pwd)"

pass=0
fail=0

report() { # name expected(FAIL|PASS) observed
  if [ "$2" = "$3" ]; then
    echo "  [ok]   $1: observed $3 (expected $2)"
    pass=$((pass + 1))
  else
    echo "  [BAD]  $1: observed $3 (expected $2)"
    fail=$((fail + 1))
  fi
}

run_test() { # pattern
  STUDIO_TEST_DB=1 STUDIO_TEST_REDIS=1 go test ./tests/integration/ ./internal/execution/ ./internal/integrations/aily/ \
    -run "$1" -count=1 2>&1 | grep -E "^(--- FAIL|--- PASS|FAIL|ok)" | head -5
}

verdict() { # pattern → FAIL | PASS
  local out
  out="$(run_test "$1")"
  if echo "$out" | grep -q "FAIL"; then echo FAIL; else echo PASS; fi
}

revert() { # file
  mv "$1.orig" "$1"
}

snapshot() { cp "$1" "$1.orig"; }

echo "=== 1. P0-1: remove the request-idempotency reservation + replay lookup ==="
snapshot internal/execution/idempotency.go
python - <<'PY'
import io
p = 'internal/execution/idempotency.go'
s = io.open(p, encoding='utf-8').read()
old = '''	run, err := s.CreateRunAdmitted(ctx, in, maxOutstanding)
	if err == nil {
		return run, false, nil
	}'''
new = '''	return s.CreateRunAdmitted(ctx, in, maxOutstanding)'''
assert old in s
s = s.replace(old, new, 1)
s = s.replace('''	if in.ClientRequestID == "" {
		return nil
	}
	res, err := q.ReserveRunRequest''', '''	if in.ClientRequestID == "" {
		return nil
	}
	if true { // FALSIFICATION: server-side reservation disabled
		return nil
	}
	res, err := q.ReserveRunRequest''', 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PY
report "idempotency replay" FAIL "$(verdict 'TestCreateRunIdempotentReplayCreatesNothingNew')"
revert internal/execution/idempotency.go
report "idempotency replay (restored)" PASS "$(verdict 'TestCreateRunIdempotentReplayCreatesNothingNew')"

echo "=== 2. P0-2: treat an unresolved submission as re-transmittable ==="
snapshot internal/execution/submission.go
python - <<'PY'
import io
p = 'internal/execution/submission.go'
s = io.open(p, encoding='utf-8').read()
old = '''		// provider, because the concurrent attempt is already on the wire.
		return nil, ErrProviderSubmitUnknown
	case SubmissionUnknown:'''
new = '''		return submissionFromRow(latest), tx.Commit() // FALSIFICATION
	case SubmissionUnknown:'''
assert old in s
s = s.replace(old, new, 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PY
report "unresolved submission never resent" FAIL "$(verdict 'TestSubmissionUnknownForbidsResend')"
report "executor parks instead of resending" FAIL "$(verdict 'TestUnresolvedSubmissionParksWithoutContactingTheProvider')"
revert internal/execution/submission.go
report "unresolved submission never resent (restored)" PASS "$(verdict 'TestSubmissionUnknownForbidsResend')"

echo "=== 3. P0-2: retry a timeout at the submit boundary (no park) ==="
snapshot internal/integrations/aily/executor.go
python - <<'PY'
import io
p = 'internal/integrations/aily/executor.go'
s = io.open(p, encoding='utf-8').read()
old = '''	return submitDisposition{Park: true, RecordOutcome: execution.SubmissionUnknown, Reason: reason}'''
new = '''	return submitDisposition{RecordOutcome: execution.SubmissionUnknown, Reason: reason} // FALSIFICATION'''
assert old in s
s = s.replace(old, new, 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PY
report "timeout parks (unit table)" FAIL "$(verdict 'TestClassifySubmitFailure')"
report "timeout parks (end to end)" FAIL "$(verdict 'TestSubmitTimeoutParksInsteadOfRetrying')"
revert internal/integrations/aily/executor.go
report "timeout parks (restored)" PASS "$(verdict 'TestSubmitTimeoutParksInsteadOfRetrying')"

echo "=== 4. P0-2: drop the already-accepted resume (resubmit instead) ==="
snapshot internal/integrations/aily/executor.go
python - <<'PY'
import io
p = 'internal/integrations/aily/executor.go'
s = io.open(p, encoding='utf-8').read()
old = '''	if claimed.Run.ExternalRunID != "" {
		e.noteSubmissionDedup()
		return &execution.ProviderSubmission{State: execution.SubmissionAccepted, ExternalRunID: claimed.Run.ExternalRunID}, submitResumeAccepted, nil
	}'''
assert old in s
s = s.replace(old, '', 1)
s = s.replace('''	if sub.State == execution.SubmissionAccepted {
		e.noteSubmissionDedup()
		return sub, submitResumeAccepted, nil
	}''', '''	if false { // FALSIFICATION: always submit
		return sub, submitResumeAccepted, nil
	}''', 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PY
report "accepted submission is not resubmitted" FAIL "$(verdict 'TestReclaimOfAcceptedSubmissionDoesNotReopenTheStream')"
revert internal/integrations/aily/executor.go
report "accepted submission is not resubmitted (restored)" PASS "$(verdict 'TestReclaimOfAcceptedSubmissionDoesNotReopenTheStream')"

echo "=== 5. P1-3: revert the sequence allocator to COUNT(*)+1 ==="
snapshot internal/execution/service.go
python - <<'PY'
import io
p = 'internal/execution/service.go'
s = io.open(p, encoding='utf-8').read()
old = '''	if _, err := q.BumpRunEventSequence(ctx, runID.Bytes()); err != nil {
		return 0, err
	}
	return sequence, nil'''
new = '''	// FALSIFICATION: the pre-第九轮 allocator.
	var cnt uint64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_events WHERE run_id = ?`, runID.Bytes()).Scan(&cnt); err != nil {
		return 0, err
	}
	return cnt + 1, nil'''
assert old in s
s = s.replace(old, new, 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PY
report "sequence allocator maintains the counter" FAIL "$(verdict 'TestEventSequenceAllocatorIsGapFreeMonotonicAndPerRun')"
revert internal/execution/service.go
report "sequence allocator maintains the counter (restored)" PASS "$(verdict 'TestEventSequenceAllocatorIsGapFreeMonotonicAndPerRun')"

echo "=== 6. P1-1: always write the SSE id (including transient frames) ==="
snapshot internal/transport/sse/sse.go
python - <<'PY'
import io
p = 'internal/transport/sse/sse.go'
s = io.open(p, encoding='utf-8').read()
old = '''		if sequence > 0 {
			fmt.Fprintf(&frame, "id: %d\\n", sequence)
		}'''
new = '''		fmt.Fprintf(&frame, "id: %d\\n", sequence) // FALSIFICATION'''
assert old in s
s = s.replace(old, new, 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PY
report "transient frames carry no resume id" FAIL "$(verdict 'TestSSETransientFrameCarriesNoResumeId')"
revert internal/transport/sse/sse.go
report "transient frames carry no resume id (restored)" PASS "$(verdict 'TestSSETransientFrameCarriesNoResumeId')"

echo "=== 7. P1-4: put the cumulative snapshot back on every chunk ==="
snapshot internal/integrations/aily/executor.go
python - <<'PY'
import io
p = 'internal/integrations/aily/executor.go'
s = io.open(p, encoding='utf-8').read()
old = '''	flushChunk := func() error {
		payload, ok := coalescer.chunk()
		if !ok {
			return nil
		}
		return e.Owned.AppendEvent(ctx, claimed, execution.EventContentChunk, payload)
	}'''
new = '''	snapshotText := &strings.Builder{} // FALSIFICATION
	flushChunk := func() error {
		payload, ok := coalescer.chunk()
		if !ok {
			return nil
		}
		payload["snapshot"] = snapshotText.String()
		return e.Owned.AppendEvent(ctx, claimed, execution.EventContentChunk, payload)
	}'''
assert old in s
s = s.replace(old, new, 1)
# 3.2-A anchor update: the delta case now computes the transient offset, so
# the snapshot accumulator hooks in next to it.
old2 = '''			text := strOf(ev.Payload["text"], "")
			endOffset, flush := coalescer.add(text)'''
new2 = '''			text := strOf(ev.Payload["text"], "")
			snapshotText.WriteString(text) // FALSIFICATION
			endOffset, flush := coalescer.add(text)'''
assert old2 in s
s = s.replace(old2, new2, 1)
s = s.replace('\t"log/slog"\n\t"time"', '\t"log/slog"\n\t"strings"\n\t"time"', 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PY
report "chunks stay incremental" FAIL "$(verdict 'TestStreamingChunkEventsAreIncremental')"
revert internal/integrations/aily/executor.go
report "chunks stay incremental (restored)" PASS "$(verdict 'TestStreamingChunkEventsAreIncremental')"

echo
echo "=== leftovers (must be empty) ==="
find . -name '*.orig' -print
echo
echo "falsifications ok=$pass bad=$fail"
[ "$fail" -eq 0 ] || exit 1
