#!/usr/bin/env bash
# Falsification driver — 第九轮「补丁批次 3.1」(复审报告 §三/§四/§五/§六/§七/§八).
#
# Same discipline as falsify_review9.sh: revert ONE fix, prove the matching
# test FAILS, restore, prove it passes again. A test that cannot fail proves
# nothing.
set -u
export PATH="/usr/bin:/bin:/c/software/Git/cmd:$PATH"
cd "$(dirname "$0")/.." || exit 1

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
  STUDIO_TEST_DB=1 STUDIO_TEST_REDIS=1 go test ./tests/integration/ ./internal/transport/http/ \
    -run "$1" -count=1 2>&1 | grep -E "^(--- FAIL|--- PASS|FAIL|ok)" | head -5
}

verdict() { # pattern → FAIL | PASS
  local out
  out="$(run_test "$1")"
  if echo "$out" | grep -q "FAIL"; then echo FAIL; else echo PASS; fi
}

revert() { mv "$1.orig" "$1"; }
snapshot() { cp "$1" "$1.orig"; }

# A falsification that is interrupted (timeout, Ctrl-C) must not leave the
# reverted fix behind — that is exactly how a "temporary" revert silently
# becomes the committed state.
cleanup() {
  for f in $(find . -name '*.orig' 2>/dev/null); do
    mv "$f" "${f%.orig}"
    echo "  [restored] ${f%.orig} (interrupted falsification)"
  done
}
trap cleanup EXIT

echo "=== 1. 复审 §四: stream ends with no chat id -> fail instead of park ==="
snapshot internal/integrations/aily/executor.go
python - <<'PYEOF'
import io
p = 'internal/integrations/aily/executor.go'
s = io.open(p, encoding='utf-8').read()
old = '''	parkIfUnconfirmed := func(reason string) (bool, error) {
		if externalRunID != "" {
			return false, nil
		}'''
new = '''	parkIfUnconfirmed := func(reason string) (bool, error) {
		if true { // FALSIFICATION: never park, reconcile("") fails the run
			return false, nil
		}'''
assert old in s
io.open(p, 'w', encoding='utf-8', newline='\n').write(s.replace(old, new, 1))
PYEOF
report "EOF before the first frame parks" FAIL "$(verdict 'TestStreamEofBeforeChatIdParksInsteadOfFailing')"
report "a frame without a chat id parks" FAIL "$(verdict 'TestFirstFrameWithoutChatIdParks')"
revert internal/integrations/aily/executor.go
report "EOF before the first frame parks (restored)" PASS "$(verdict 'TestStreamEofBeforeChatIdParksInsteadOfFailing')"

echo "=== 2. 复审 §四: emit stream.started on ANY first frame ==="
snapshot internal/integrations/aily/adapter.go
python - <<'PYEOF'
import io
p = 'internal/integrations/aily/adapter.go'
s = io.open(p, encoding='utf-8').read()
old = '''				chatID, _ := parsed["agent_chat_id"].(string)
				if chatID == "" {
					// Fall through: this frame's own events are still
					// delivered, we just do not claim to have started.
					return emitParsed(mapper, eventName, parsed, cctx, out)
				}
				started = true'''
new = '''				chatID, _ := parsed["agent_chat_id"].(string)
				started = true // FALSIFICATION: a frame is not an identity
				_ = chatID'''
assert old in s
io.open(p, 'w', encoding='utf-8', newline='\n').write(s.replace(old, new, 1))
PYEOF
report "a chat id on a later frame still resumes" FAIL "$(verdict 'TestChatIdOnALaterFrameStillResumes')"
revert internal/integrations/aily/adapter.go
report "a chat id on a later frame still resumes (restored)" PASS "$(verdict 'TestChatIdOnALaterFrameStillResumes')"

echo "=== 3. 复审 §五: MarkSubmissionStateOwned without the ownership fence ==="
snapshot internal/execution/submission.go
python - <<'PYEOF'
import io
p = 'internal/execution/submission.go'
s = io.open(p, encoding='utf-8').read()
old = '''	if _, err := verifyActiveOwnershipTx(ctx, tx, own); err != nil {
		return err
	}
	q := db.New(tx)
	res, err := q.MarkProviderSubmissionState(ctx, db.MarkProviderSubmissionStateParams{'''
new = '''	// FALSIFICATION: the fence is gone; own.Valid() alone only proves the
	// token is well formed.
	q := db.New(tx)
	res, err := q.MarkProviderSubmissionState(ctx, db.MarkProviderSubmissionStateParams{'''
assert old in s
io.open(p, 'w', encoding='utf-8', newline='\n').write(s.replace(old, new, 1))
PYEOF
report "submission state write is fenced" FAIL "$(verdict 'TestMarkSubmissionStateRequiresLiveOwnershipAndLegalTransition')"
revert internal/execution/submission.go
report "submission state write is fenced (restored)" PASS "$(verdict 'TestMarkSubmissionStateRequiresLiveOwnershipAndLegalTransition')"

echo "=== 4. 复审 §五: drop the state CAS (any state may be overwritten) ==="
snapshot internal/gen/db/execution.sql.go
python - <<'PYEOF'
import io
p = 'internal/gen/db/execution.sql.go'
s = io.open(p, encoding='utf-8').read()
old = "WHERE run_id = ? AND submission_no = ? AND state = 'sending'\n"
new = "WHERE run_id = ? AND submission_no = ? -- FALSIFICATION: no CAS\n"
assert old in s
io.open(p, 'w', encoding='utf-8', newline='\n').write(s.replace(old, new, 1))
PYEOF
report "submission state transition is a CAS" FAIL "$(verdict 'TestMarkSubmissionStateRequiresLiveOwnershipAndLegalTransition')"
revert internal/gen/db/execution.sql.go
report "submission state transition is a CAS (restored)" PASS "$(verdict 'TestMarkSubmissionStateRequiresLiveOwnershipAndLegalTransition')"

echo "=== 5. 复审 §七: leave the round-nine tables out of the cascade ==="
snapshot internal/execution/conversation.go
python - <<'PYEOF'
import io
p = 'internal/execution/conversation.go'
s = io.open(p, encoding='utf-8').read()
old = '''		if err := q.DeleteRunRequestsByRun(ctx, runID); err != nil {
			return fmt.Errorf("conversation delete: run requests: %w", err)
		}
		if err := q.DeleteProviderSubmissionsByRun(ctx, runID); err != nil {
			return fmt.Errorf("conversation delete: provider submissions: %w", err)
		}
'''
assert old in s
io.open(p, 'w', encoding='utf-8', newline='\n').write(s.replace(old, '', 1))
PYEOF
report "cascade deletes run_requests + provider_submissions" FAIL "$(verdict 'TestCascadeDeleteRemovesRoundNineChildTables')"
revert internal/execution/conversation.go
report "cascade deletes run_requests + provider_submissions (restored)" PASS "$(verdict 'TestCascadeDeleteRemovesRoundNineChildTables')"

echo "=== 6. 复审 §八: a native-idempotent provider may not resend ==="
snapshot internal/execution/submission.go
python - <<'PYEOF'
import io
p = 'internal/execution/submission.go'
s = io.open(p, encoding='utf-8').read()
old = '''	case SubmissionUnknown:
		if !resendOnUnknown {
			return nil, ErrProviderSubmitUnknown
		}'''
new = '''	case SubmissionUnknown:
		if true { // FALSIFICATION: the capability is ignored
			return nil, ErrProviderSubmitUnknown
		}'''
assert old in s
io.open(p, 'w', encoding='utf-8', newline='\n').write(s.replace(old, new, 1))
PYEOF
report "native idempotency may resend on the same key" FAIL "$(verdict 'TestNativeIdempotencyMayResendOnTheSameKey')"
revert internal/execution/submission.go
report "native idempotency may resend on the same key (restored)" PASS "$(verdict 'TestNativeIdempotencyMayResendOnTheSameKey')"

echo "=== 7. 复审 §六: no wait for an in-flight duplicate's reservation ==="
snapshot internal/execution/idempotency.go
python - <<'PYEOF'
import io
p = 'internal/execution/idempotency.go'
s = io.open(p, encoding='utf-8').read()
old = '''	deadline := time.Now().Add(wait)'''
new = '''	deadline := time.Now() // FALSIFICATION: one read, no wait'''
assert old in s
io.open(p, 'w', encoding='utf-8', newline='\n').write(s.replace(old, new, 1))
PYEOF
report "resolve waits for the winner's commit" FAIL "$(verdict 'TestResolveRunRequestWithWaitBridgesAnUncommittedReservation')"
revert internal/execution/idempotency.go
report "resolve waits for the winner's commit (restored)" PASS "$(verdict 'TestResolveRunRequestWithWaitBridgesAnUncommittedReservation')"

echo
echo "=== leftovers (must be empty) ==="
find . -name '*.orig' -print
echo
echo "falsifications ok=$pass bad=$fail"
[ "$fail" -eq 0 ] || exit 1
