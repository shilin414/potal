#!/usr/bin/env bash
# Falsification driver — 第九轮「补丁批次 3.2」(复审报告 §十七).
#
# Three mutations, one per 3.2 fix; each must make its matching test FAIL,
# then PASS again after the fix is restored:
#   1. restore "background submit success without a chat id goes straight to
#      poll"        → TestBackgroundSubmitSuccessWithoutChatIDParks FAILs
#   2. restore "accepted-persistence error is log-and-continue"
#                    → TestBackgroundAcceptedPersistenceFailureStopsExecution FAILs
#      (the streaming twin breaks with it; one mutation, one verdict)
# Same discipline as the earlier drivers; a test that cannot fail proves
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
  STUDIO_TEST_DB=1 STUDIO_TEST_REDIS=1 go test ./tests/integration/ \
    -run "$1" -count=1 2>&1 | grep -E "^(--- FAIL|--- PASS|FAIL|ok)" | head -5
}

verdict() { # pattern → FAIL | PASS
  local out
  out="$(run_test "$1")"
  if echo "$out" | grep -q "FAIL"; then echo FAIL; else echo PASS; fi
}

revert() { mv "$1.orig" "$1"; }
snapshot() { cp "$1" "$1.orig"; }

cleanup() {
  for f in $(find . -name '*.orig' 2>/dev/null); do
    mv "$f" "${f%.orig}"
    echo "  [restored] ${f%.orig} (interrupted falsification)"
  done
}
trap cleanup EXIT

echo "=== 1. 复审 §五: background submit success without a chat id goes straight to poll ==="
snapshot internal/integrations/aily/executor.go
python - <<'PYEOF'
import io
p = 'internal/integrations/aily/executor.go'
s = io.open(p, encoding='utf-8').read()
old = '\tif result == nil || result.ExternalRunID == "" {'
new = '\tif false && (result == nil || result.ExternalRunID == "") { // FALSIFICATION'
assert old in s, "mutation 1 anchor missing"
s = s.replace(old, new, 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "background 200-without-chat-id parks (mutated)" FAIL "$(verdict 'TestBackgroundSubmitSuccessWithoutChatIDParks')"
revert internal/integrations/aily/executor.go
report "background 200-without-chat-id parks (restored)" PASS "$(verdict 'TestBackgroundSubmitSuccessWithoutChatIDParks')"

echo "=== 2. 复审 §六/§七: accepted-persistence failure is log-and-continue again ==="
snapshot internal/integrations/aily/executor.go
python - <<'PYEOF'
import io
p = 'internal/integrations/aily/executor.go'
s = io.open(p, encoding='utf-8').read()
old = '\treturn fmt.Errorf("%w: %w", ErrProviderAcceptancePersistence, err)'
new = '\treturn nil // FALSIFICATION: log-and-continue'
assert old in s, "mutation 2 anchor missing"
s = s.replace(old, new, 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "accepted-persistence failure stops the chain (mutated)" FAIL "$(verdict 'TestBackgroundAcceptedPersistenceFailureStopsExecution')"
revert internal/integrations/aily/executor.go
report "accepted-persistence failure stops the chain (restored)" PASS "$(verdict 'TestBackgroundAcceptedPersistenceFailureStopsExecution')"

echo
echo "=== leftovers (must be empty) ==="
find . -name '*.orig' -print
echo
echo "falsifications ok=$pass bad=$fail"
[ "$fail" -eq 0 ] || exit 1
