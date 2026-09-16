#!/usr/bin/env bash
# Falsification driver — 第九轮「补丁批次 3.3」(复审报告 §三十八).
#
# Four mutations, one per 3.3 fix; each must make its matching test FAIL, then
# PASS again after the fix is restored:
#
#   1. restore "capacity = active provider slot count"
#        → TestProviderCapacityHoldsAfterUnknownParkReleasesSlot FAILs
#          (the parked run holds NO slot, so an unresolved provider request
#           would stop occupying capacity)
#   2. make the admission capacity check INCLUDE the run being decided
#        → TestProviderCapacityLetsTheSameUnresolvedRunRecover FAILs
#          (an unresolved run rejects itself: self-deadlock)
#   3. drop 'accepted' from the capacity submission states
#        → TestProviderCapacityHoldsAfterAcceptedWorkerCrash FAILs
#          (a crashed worker's still-running provider chat stops counting)
#   4. let the hub subscriber deliver transient content.delta to legacy
#      protocol clients again
#        → TestHubSubscriberProtocolIsolation FAILs
#          (a legacy subscriber receives content.delta on top of the durable
#           chunk and renders the overlap twice)
#
# Same discipline as the earlier drivers; a test that cannot fail proves
# nothing.
#
# Mutation 4 maintenance (Batch 5.1, P2-4): the capability gate used to live
# in internal/transport/sse/sse.go's gateway frame loop and was proven by the
# integration test TestSSELegacyClientReceivesNoTransientDelta. The SSE Hub
# (Batch 4) moved the gate into Subscriber.accepts
# (internal/transport/sse/hub_subscription.go) and the contract is now pinned
# by TestHubSubscriberProtocolIsolation (legacy vs protocol-2 fan-out). The
# mutation retargets THAT code and THAT test — production code stays
# untouched.
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

run_pkg_test() { # package pattern
  go test "$1" -run "$2" -count=1 -timeout 300s 2>&1 \
    | grep -E "^(--- FAIL|--- PASS|FAIL|ok)" | head -5
}

pkg_verdict() { # package pattern → FAIL | PASS
  local out
  out="$(run_pkg_test "$1" "$2")"
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

# Both mutations 1 and 2 rewrite the SAME call site in the admission decision
# (the effective-capacity query excludes the run being decided). Each is
# snapshotted and reverted before the next one is applied, so the anchors are
# kept inline and explicit rather than shared.

echo "=== 1. §九/§二十一 Case 2: capacity goes back to 'active provider slot count' ==="
snapshot internal/execution/slots.go
python - <<'PYEOF'
import io
p = 'internal/execution/slots.go'
s = io.open(p, encoding='utf-8').read()
anchor = (
    '\tother, err := q.CountProviderEffectiveInflightExcludingRun(ctx,\n'
    '\t\tdb.CountProviderEffectiveInflightExcludingRunParams{\n'
    '\t\t\tProvider:   l.Provider,\n'
    '\t\t\tRunID:      own.RunID.Bytes(),\n'
    '\t\t\tProvider_2: l.Provider,\n'
    '\t\t\tRunID_2:    own.RunID.Bytes(),\n'
    '\t\t})\n'
    '\tif err != nil {\n'
    '\t\treturn nil, false, 0, err\n'
    '\t}\n'
)
assert anchor in s, "mutation 1 anchor missing"
new = (
    '\tother, err := q.CountActiveProviderSlots(ctx, l.Provider) // FALSIFICATION: controlled slots only\n'
    '\tif err != nil {\n'
    '\t\treturn nil, false, 0, err\n'
    '\t}\n'
)
s = s.replace(anchor, new, 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "unknown outcome keeps occupying capacity (mutated)" FAIL "$(verdict 'TestProviderCapacityHoldsAfterUnknownParkReleasesSlot')"
revert internal/execution/slots.go
report "unknown outcome keeps occupying capacity (restored)" PASS "$(verdict 'TestProviderCapacityHoldsAfterUnknownParkReleasesSlot')"

echo "=== 2. §十/§十一: the capacity check includes the run being decided ==="
snapshot internal/execution/slots.go
python - <<'PYEOF'
import io
p = 'internal/execution/slots.go'
s = io.open(p, encoding='utf-8').read()
anchor = (
    '\tother, err := q.CountProviderEffectiveInflightExcludingRun(ctx,\n'
    '\t\tdb.CountProviderEffectiveInflightExcludingRunParams{\n'
    '\t\t\tProvider:   l.Provider,\n'
    '\t\t\tRunID:      own.RunID.Bytes(),\n'
    '\t\t\tProvider_2: l.Provider,\n'
    '\t\t\tRunID_2:    own.RunID.Bytes(),\n'
    '\t\t})\n'
)
assert anchor in s, "mutation 2 anchor missing"
new = (
    '\tother, err := q.CountProviderEffectiveInflight(ctx, // FALSIFICATION: includes self\n'
    '\t\tdb.CountProviderEffectiveInflightParams{\n'
    '\t\t\tProvider:   l.Provider,\n'
    '\t\t\tProvider_2: l.Provider,\n'
    '\t\t})\n'
)
s = s.replace(anchor, new, 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "unresolved run can re-acquire its own capacity (mutated)" FAIL "$(verdict 'TestProviderCapacityLetsTheSameUnresolvedRunRecover')"
revert internal/execution/slots.go
report "unresolved run can re-acquire its own capacity (restored)" PASS "$(verdict 'TestProviderCapacityLetsTheSameUnresolvedRunRecover')"

echo "=== 3. §五: 'accepted' stops counting as provider capacity ==="
# Mutating the SQL TEXT the driver sends is what the state list means; the
# checked-in generated code is that text, and re-running sqlc would produce
# exactly this file back on revert anyway.
snapshot internal/gen/db/execution.sql.go
python - <<'PYEOF'
import io
p = 'internal/gen/db/execution.sql.go'
s = io.open(p, encoding='utf-8').read()
old = "AND ps.state IN ('sending', 'unknown', 'accepted')"
assert old in s, "mutation 3 anchor missing"
s = s.replace(old, "AND ps.state IN ('sending', 'unknown')")  # FALSIFICATION
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "accepted provider chat keeps occupying capacity (mutated)" FAIL "$(verdict 'TestProviderCapacityHoldsAfterAcceptedWorkerCrash')"
revert internal/gen/db/execution.sql.go
report "accepted provider chat keeps occupying capacity (restored)" PASS "$(verdict 'TestProviderCapacityHoldsAfterAcceptedWorkerCrash')"

echo "=== 4. §二十四: legacy clients get transient content.delta again ==="
# Retargeted (Batch 5.1, P2-4): the gate now lives in Subscriber.accepts
# (internal/transport/sse/hub_subscription.go) — mutated by switching the
# protocol comparison to a constant false, so legacy subscribers receive the
# transient delta they must never see. The contract test is the sse package's
# TestHubSubscriberProtocolIsolation.
snapshot internal/transport/sse/hub_subscription.go
python - <<'PYEOF'
import io
p = 'internal/transport/sse/hub_subscription.go'
s = io.open(p, encoding='utf-8').read()
old = (
    '\tif ev.Sequence == 0 && ev.EventType == execution.EventContentDelta &&\n'
    '\t\ts.protocol < StreamProtocolRangeDelta {\n'
)
assert s.count(old) == 1, "mutation 4 anchor missing or ambiguous"
new = (
    '\tif ev.Sequence == 0 && ev.EventType == execution.EventContentDelta &&\n'
    '\t\tfalse { // FALSIFICATION: capability gate removed\n'
)
s = s.replace(old, new, 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "legacy subscriber receives no transient delta (mutated)" FAIL \
  "$(pkg_verdict ./internal/transport/sse/ TestHubSubscriberProtocolIsolation)"
revert internal/transport/sse/hub_subscription.go
report "legacy subscriber receives no transient delta (restored)" PASS \
  "$(pkg_verdict ./internal/transport/sse/ TestHubSubscriberProtocolIsolation)"

echo
echo "=== leftovers (both must be empty) ==="
grep -rn 'FALSIFICATION' . --include='*.go' --include='*.sql' || echo "  (no FALSIFICATION markers)"
find . -name '*.orig' -print || true
if grep -rq 'FALSIFICATION' . --include='*.go' --include='*.sql'; then
  echo "  [BAD] a FALSIFICATION marker survived the run"
  fail=$((fail + 1))
fi
if [ -n "$(find . -name '*.orig' -print 2>/dev/null)" ]; then
  echo "  [BAD] a .orig backup survived the run"
  fail=$((fail + 1))
fi
echo
echo "falsifications ok=$pass bad=$fail"
[ "$fail" -eq 0 ] || exit 1
