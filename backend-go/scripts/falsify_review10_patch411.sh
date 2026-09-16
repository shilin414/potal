#!/usr/bin/env bash
# Falsification driver — 第十轮 Batch 4.1.1「Hub Initial Timer Race Closure」
# (复审报告 §7-§24).
#
# Three mutations, one per lifecycle defence the patch claims to have closed;
# each must make its matching test FAIL, then PASS again after the code is
# restored (Batch 4.1.2 added N, the subscriber guard):
#
#   L. the initial idle timer goes back INSIDE the newRunHub constructor (§24)
#        → TestNewRunHubIsInertBeforePublication FAIL
#      (the pre-4.1.1 shape: `armIdleTimerLocked` calls time.AfterFunc, whose
#       callback clears `idleTimer`/`closed` — so the constructor races the
#       timer it just created, and `SSE_HUB_IDLE_TTL=1ns` lets the callback win)
#   M. repairDurableGap stops checking that the canonical log is contiguous
#      (§24)
#        → TestGatewayFailsClosedOnCanonicalLogSequenceHole FAIL
#      (a hole in the log is stepped over and the client's cursor lands past an
#       event it never saw)
#   N. armInitialIdleTimer drops its "a subscriber is already attached" guard
#      (Batch 4.1.2, review §10/§12)
#        → TestArmInitialIdleTimerSkipsAlreadySubscribedHub FAIL
#      (the initial timer is armed on a hub that already has a subscriber, so the
#       reuse window ends at a deadline fixed BEFORE that subscriber existed.
#       Nothing is lost — the callback re-checks the subscriber set — but a
#       subscriber that leaves inside the initial TTL no longer gets a full
#       IdleTTL, which costs Redis SUBSCRIBE churn and extra hub generations
#       under a reconnect storm. Hence P2, and hence a root-cause assertion on
#       `idleTimer == nil` rather than a wall-clock retention window.)
#
# Mutation N has no second half in the race gate: dropping the guard produces no
# unsynchronised access (every reader of the field still holds hub.mu), so
# `go test -race` stays green. Only the deterministic interleaving test can see
# it — which is exactly why AC-4.1.2-2..5 exist.
#
# Mutation M is deliberately NOT paired with the positive control
# (TestGatewayRepairsGapFromContiguousLog): that test passes both before and
# after the mutation by design — it exists to prove the repair still REPAIRS,
# so "refuse everything" cannot satisfy the hole test. Only the hole test is an
# anti-test.
#
# Mutation L cannot be caught by the functional half of the suite alone: the
# data race it reintroduces is exactly what `TestTinyInitialIdleTTLIsRaceSafe`
# exists to hand to the race detector, so the CI race gate is the second half
# of this mutation's verdict. Locally (CGO_ENABLED=0, no gcc) only the
# functional half runs; the invariant assertion is what fails here.
#
# Same discipline as the earlier drivers; a test that cannot fail proves
# nothing.
#
# ⚠️ NEVER run this driver concurrently with another `go test` invocation.
# It mutates REAL source files, so any test binary that is built while a
# mutation is in place compiles the broken code — a second suite running in
# parallel can then fail for a reason that has nothing to do with what it is
# testing.
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

run_pkg() { # package pattern
  go test "$1" -run "$2" -count=1 -timeout 300s 2>&1 \
    | grep -E "^(--- FAIL|--- PASS|FAIL|ok)" | head -5
}

verdict() { # package pattern → FAIL | PASS
  local out
  out="$(run_pkg "$1" "$2")"
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

PKG=./internal/transport/sse/
INERT_TESTS='TestNewRunHubIsInertBeforePublication|TestGetOrCreateArmsInitialIdleTimer|TestSubscriberStopsArmedInitialIdleTimer'
# Mutation N targets the subscriber guard, so its test must be the one that
# BUILDS "subscriber attached → arm" instead of hoping a scheduler produces it.
GUARD_TESTS='TestArmInitialIdleTimerSkipsAlreadySubscribedHub'
HOLE_TESTS='TestGatewayFailsClosedOnCanonicalLogSequenceHole'

echo "=== L. §24: the initial idle timer is armed inside the constructor again ==="
snapshot internal/transport/sse/hub.go
python - <<'PYEOF'
import io
p = 'internal/transport/sse/hub.go'
s = io.open(p, encoding='utf-8').read()

# (1) give the literal a name so the constructor can arm a timer on it.
old = (
    "func newRunHub(m *HubManager, runID string) *RunHub {\n"
    "\tctx, cancel := context.WithCancel(m.ctx)\n"
    "\treturn &RunHub{\n"
)
assert old in s, "mutation L anchor 1 missing"
new = (
    "func newRunHub(m *HubManager, runID string) *RunHub {\n"
    "\tctx, cancel := context.WithCancel(m.ctx)\n"
    "\thub := &RunHub{\n"
)
s = s.replace(old, new, 1)

# (2) arm the timer BEFORE the hub is published — the pre-4.1.1 shape.
old2 = (
    "\t\tcache:       NewDurableRing(m.opts.CacheMaxEvents, m.opts.CacheMaxBytes),\n"
    "\t}\n"
    "}\n"
)
assert old2 in s, "mutation L anchor 2 missing"
new2 = (
    "\t\tcache:       NewDurableRing(m.opts.CacheMaxEvents, m.opts.CacheMaxBytes),\n"
    "\t}\n"
    "\t// FALSIFICATION: arm the initial idle timer in the constructor, without\n"
    "\t// hub.mu and before publication — time.AfterFunc hands the callback to\n"
    "\t// another goroutine immediately, so it races this very assignment.\n"
    "\thub.armIdleTimerLocked()\n"
    "\treturn hub\n"
    "}\n"
)
s = s.replace(old2, new2, 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "an unpublished hub is inert (mutated)" FAIL \
  "$(verdict $PKG "$INERT_TESTS")"
revert internal/transport/sse/hub.go
report "an unpublished hub is inert (restored)" PASS \
  "$(verdict $PKG "$INERT_TESTS")"

echo "=== M. §24: the repair stops verifying that the log is contiguous ==="
snapshot internal/transport/sse/sse.go
python - <<'PYEOF'
import io
p = 'internal/transport/sse/sse.go'
s = io.open(p, encoding='utf-8').read()
start = s.index("\t\t\tif ev.Sequence != last+1 {")
end = s.index("\t\t\tif !writeFrame(ev.Sequence, ev.EventType, ev.Payload, false) {", start)
assert start < end, "mutation M anchors out of order"
s = s[:start] + s[end:]
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "a hole in the canonical log fails closed (mutated)" FAIL \
  "$(verdict $PKG "$HOLE_TESTS")"
revert internal/transport/sse/sse.go
report "a hole in the canonical log fails closed (restored)" PASS \
  "$(verdict $PKG "$HOLE_TESTS")"

echo "=== N. §10/§12: armInitialIdleTimer no longer defers to an attached subscriber ==="
snapshot internal/transport/sse/hub.go
python - <<'PYEOF'
import io
p = 'internal/transport/sse/hub.go'
s = io.open(p, encoding='utf-8').read()
old = (
    "\tif h.closed {\n"
    "\t\treturn\n"
    "\t}\n"
    "\tif len(h.subscribers) != 0 {\n"
    "\t\treturn\n"
    "\t}\n"
    "\th.armIdleTimerLocked()\n"
    "}\n"
)
assert s.count(old) == 1, "mutation N anchor missing or ambiguous"
new = (
    "\tif h.closed {\n"
    "\t\treturn\n"
    "\t}\n"
    "\t// FALSIFICATION: the subscriber guard is gone, so a hub that already has\n"
    "\t// a subscriber still gets an idle timer armed on it. The callback still\n"
    "\t// re-checks the subscriber set, so nothing is lost outright — but the\n"
    "\t// hub's reuse window now ends at a deadline fixed before that subscriber\n"
    "\t// existed, and a detach inside the window no longer restarts a full\n"
    "\t// IdleTTL.\n"
    "\th.armIdleTimerLocked()\n"
    "}\n"
)
s = s.replace(old, new, 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "a subscriber already attached suppresses the initial arm (mutated)" FAIL \
  "$(verdict $PKG "$GUARD_TESTS")"
revert internal/transport/sse/hub.go
report "a subscriber already attached suppresses the initial arm (restored)" PASS \
  "$(verdict $PKG "$GUARD_TESTS")"

echo "=== control: the gap repair still repairs a gap-free log ==="
report "a gap-free log is still repaired end to end" PASS \
  "$(verdict $PKG TestGatewayRepairsGapFromContiguousLog)"

echo
echo "=== leftovers (all three must be empty) ==="
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
