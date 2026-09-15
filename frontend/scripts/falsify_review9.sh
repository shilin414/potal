#!/usr/bin/env bash
# Frontend falsification (第九轮): revert one frontend fix at a time, prove the
# matching test FAILS, restore, prove it passes again.
set -u
export PATH="/usr/bin:/bin:/c/software/Git/cmd:$HOME/.local/bin:$PATH"
cd "$(dirname "$0")/.." || exit 1

pass=0
fail=0
report() {
  if [ "$2" = "$3" ]; then
    echo "  [ok]   $1: observed $3 (expected $2)"
    pass=$((pass + 1))
  else
    echo "  [BAD]  $1: observed $3 (expected $2)"
    fail=$((fail + 1))
  fi
}
verdict() { # test-file
  if npx vitest run "$1" >/tmp/vitest.out 2>&1; then echo PASS; else echo FAIL; fi
}

echo "=== 1. drop the resume cursor (always replay from 0) ==="
cp src/services/runStream.ts src/services/runStream.ts.orig
python - <<'PY'
import io
p = 'src/services/runStream.ts'
s = io.open(p, encoding='utf-8').read()
old = "        const cursor = lastDurableSequence > 0 ? `?after=${lastDurableSequence}` : '';"
new = "        const cursor = ''; // FALSIFICATION"
assert old in s
with io.open(p, 'w', encoding='utf-8', newline='\n') as fh:
    fh.write(s.replace(old, new, 1))
PY
report "reconnect resumes from the cursor" FAIL "$(verdict src/services/__tests__/runStream.test.ts)"
mv src/services/runStream.ts.orig src/services/runStream.ts
report "reconnect resumes from the cursor (restored)" PASS "$(verdict src/services/__tests__/runStream.test.ts)"

echo "=== 2. drop the local dedupe guard ==="
cp src/services/runStream.ts src/services/runStream.ts.orig
python - <<'PY'
import io
p = 'src/services/runStream.ts'
s = io.open(p, encoding='utf-8').read()
old = "    if (sequence > 0 && sequence <= lastDurableSequence) {"
new = "    if (false) { // FALSIFICATION"
assert old in s
with io.open(p, 'w', encoding='utf-8', newline='\n') as fh:
    fh.write(s.replace(old, new, 1))
PY
report "duplicate frames are dropped" FAIL "$(verdict src/services/__tests__/runStream.test.ts)"
mv src/services/runStream.ts.orig src/services/runStream.ts
report "duplicate frames are dropped (restored)" PASS "$(verdict src/services/__tests__/runStream.test.ts)"

echo "=== 3. let a transient sentinel move the cursor (durable → transient) ==="
cp src/services/runStream.ts src/services/runStream.ts.orig
python - <<'PY'
import io
p = 'src/services/runStream.ts'
s = io.open(p, encoding='utf-8').read()
old = "    if (sequence > 0) lastDurableSequence = sequence;"
new = "    if (sequence >= 0) lastDurableSequence = sequence; // FALSIFICATION"
assert old in s
with io.open(p, 'w', encoding='utf-8', newline='\n') as fh:
    fh.write(s.replace(old, new, 1))
PY
report "transient frames do not move the cursor" FAIL "$(verdict src/services/__tests__/runStream.test.ts)"
mv src/services/runStream.ts.orig src/services/runStream.ts
report "transient frames do not move the cursor (restored)" PASS "$(verdict src/services/__tests__/runStream.test.ts)"

echo "=== 4. stop sending client_request_id ==="
cp src/stores/useRunChatStore.ts src/stores/useRunChatStore.ts.orig
python - <<'PY'
import io
p = 'src/stores/useRunChatStore.ts'
s = io.open(p, encoding='utf-8').read()
old = "        clientRequestId: requestId,"
new = "        // FALSIFICATION: no idempotency identity"
assert old in s
with io.open(p, 'w', encoding='utf-8', newline='\n') as fh:
    fh.write(s.replace(old, new, 1))
PY
report "the send carries an idempotency identity" FAIL "$(verdict src/stores/__tests__/useRunChatStore.idempotency.test.ts)"
mv src/stores/useRunChatStore.ts.orig src/stores/useRunChatStore.ts
report "the send carries an idempotency identity (restored)" PASS "$(verdict src/stores/__tests__/useRunChatStore.idempotency.test.ts)"

echo "=== 5. forget the failed send's identity (always a fresh id) ==="
cp src/stores/useRunChatStore.ts src/stores/useRunChatStore.ts.orig
python - <<'PY'
import io
p = 'src/stores/useRunChatStore.ts'
s = io.open(p, encoding='utf-8').read()
old = """      || (pendingSend && pendingSend.fingerprint === fingerprint
        ? pendingSend.clientRequestId
        : newClientRequestId());"""
new = """      || newClientRequestId(); // FALSIFICATION"""
assert old in s
with io.open(p, 'w', encoding='utf-8', newline='\n') as fh:
    fh.write(s.replace(old, new, 1))
PY
report "a retry reuses the failed send's id" FAIL "$(verdict src/stores/__tests__/useRunChatStore.idempotency.test.ts)"
mv src/stores/useRunChatStore.ts.orig src/stores/useRunChatStore.ts
report "a retry reuses the failed send's id (restored)" PASS "$(verdict src/stores/__tests__/useRunChatStore.idempotency.test.ts)"

echo "=== 6. insert the turn unconditionally (duplicate on replay) ==="
cp src/stores/useRunChatStore.ts src/stores/useRunChatStore.ts.orig
python - <<'PY'
import io
p = 'src/stores/useRunChatStore.ts'
s = io.open(p, encoding='utf-8').read()
old = "      const alreadyPresent = conv.messages.some((m) => m.id === userMsg.id);"
new = "      const alreadyPresent = false; // FALSIFICATION"
assert old in s
with io.open(p, 'w', encoding='utf-8', newline='\n') as fh:
    fh.write(s.replace(old, new, 1))
PY
report "a replay does not duplicate the turn" FAIL "$(verdict src/stores/__tests__/useRunChatStore.idempotency.test.ts)"
mv src/stores/useRunChatStore.ts.orig src/stores/useRunChatStore.ts
report "a replay does not duplicate the turn (restored)" PASS "$(verdict src/stores/__tests__/useRunChatStore.idempotency.test.ts)"

echo "=== 7. append every durable chunk (ignore the byte offset) ==="
cp src/stores/useRunChatStore.ts src/stores/useRunChatStore.ts.orig
python - <<'PY'
import io
p = 'src/stores/useRunChatStore.ts'
s = io.open(p, encoding='utf-8').read()
old = "        message.streamBytes = applyIncrementalRange(message, event.payload || {});"
new = """        message.content += event.payload?.text || ''; // FALSIFICATION
        message.streamBytes = utf8ByteLength(message.content);"""
assert old in s
with io.open(p, 'w', encoding='utf-8', newline='\n') as fh:
    fh.write(s.replace(old, new, 1))
PY
report "durable chunks reconcile against the rendered offset" FAIL "$(verdict src/stores/__tests__/useRunChatStore.test.ts)"
mv src/stores/useRunChatStore.ts.orig src/stores/useRunChatStore.ts
report "durable chunks reconcile against the rendered offset (restored)" PASS "$(verdict src/stores/__tests__/useRunChatStore.test.ts)"

echo
echo "=== leftovers (must be empty) ==="
find src -name '*.orig' -print
echo
echo "frontend falsifications ok=$pass bad=$fail"
[ "$fail" -eq 0 ] || exit 1
