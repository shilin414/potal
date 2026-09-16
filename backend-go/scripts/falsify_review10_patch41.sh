#!/usr/bin/env bash
# Falsification driver — 第十轮 Batch 4.1「SSE Hub Correctness Closure」
# (复审报告 §32/§33/§34).
#
# Four mutations, one per defect the patch claims to have closed; each must make
# its matching test FAIL, then PASS again after the code is restored:
#
#   H. the live loop writes the frame Redis handed it instead of repairing the
#      forward gap (§12)
#        → TestGatewayRepairsOutOfOrderDurableLiveEvent /
#          TestGatewayRepairsGapBeforeTerminal FAIL
#      (a client that renders 102 before 101 resumes from 102 and loses 101 —
#       forever, because the cursor it stores is the highest sequence it SAW)
#   I. a failed gap repair still writes the frame that exposed the hole (§13)
#        → TestGatewayFailsClosedWhenGapRepairFails FAIL
#      (the hole is passed on to the client instead of ending the connection)
#   J. the pre-4.1 cache rules return: an event bigger than the whole byte
#      budget is retained, and eviction stops at one entry (§20)
#        → TestDurableRingRejectsSingleOversizedEvent /
#          TestDurableRingBytesNeverExceedConfiguredBound FAIL
#      (SSE_HUB_CACHE_BYTES stops being a memory bound)
#   K. the cache-metric generation fence is removed (§25)
#        → TestStaleHubCannotRestoreCacheMetricsAfterEviction /
#          TestOldHubCannotOverwriteNewHubCacheMetrics FAIL
#      (an evicted hub resurrects its gauges: hubs_active=0 with cache_bytes>0)
#
# Mutation J changes TWO places on purpose, exactly like Mutation G in the
# Batch 4 driver: the byte bound is enforced twice — the oversized event is
# refused before it is appended, and eviction is allowed to empty the ring — and
# either half alone still holds the bound. "The newest entry always survives" is
# the one defect, so it is one mutation.
#
# Same discipline as the earlier drivers; a test that cannot fail proves
# nothing.
#
# ⚠️ NEVER run this driver concurrently with another `go test` invocation.
# It mutates REAL source files, so any test binary that is built while a
# mutation is in place compiles the broken code — a second suite running in
# parallel can then fail for a reason that has nothing to do with what it is
# testing. (Observed once: a full integration run overlapped this driver and
# failed with an unrelated schedule/reaper error; three clean sequential runs
# after it passed.)
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
GAP_TESTS='TestGatewayRepairsOutOfOrderDurableLiveEvent|TestGatewayRepairsGapBeforeTerminal'
SIZE_TESTS='TestDurableRingRejectsSingleOversizedEvent|TestDurableRingBytesNeverExceedConfiguredBound|TestDurableRingOversizedEventBreaksCoverage'
METRIC_TESTS='TestStaleHubCannotRestoreCacheMetricsAfterEviction|TestOldHubCannotOverwriteNewHubCacheMetrics'

echo "=== H. §12: the live loop writes the out-of-order frame instead of repairing ==="
snapshot internal/transport/sse/sse.go
python - <<'PYEOF'
import io
p = 'internal/transport/sse/sse.go'
s = io.open(p, encoding='utf-8').read()
old = (
    "\t\t\tnewLast, terminal, ok := g.repairDurableGap(ctx, run, hub, lastDelivered, ev.Sequence, writeFrame)\n"
    "\t\t\tif !ok {\n"
    "\t\t\t\t// Fail closed: end the transport and let the client\n"
    "\t\t\t\t// reconnect from its last contiguous cursor. Losing the\n"
    "\t\t\t\t// connection costs a reconnect; guessing here loses content.\n"
    "\t\t\t\treturn\n"
    "\t\t\t}\n"
    "\t\t\tlastDelivered = newLast\n"
    "\t\t\tif terminal {\n"
    "\t\t\t\treturn\n"
    "\t\t\t}\n"
)
assert old in s, "mutation H anchor missing"
new = (
    "\t\t\t// FALSIFICATION: write the frame Redis handed us, hole and all —\n"
    "\t\t\t// the client's cursor then jumps past bytes it never saw.\n"
    "\t\t\tif !writeFrame(ev.Sequence, ev.EventType, ev.Payload, true) {\n"
    "\t\t\t\treturn\n"
    "\t\t\t}\n"
    "\t\t\tlastDelivered = ev.Sequence\n"
    "\t\t\tif ev.IsTerminal() {\n"
    "\t\t\t\treturn\n"
    "\t\t\t}\n"
)
s = s.replace(old, new, 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "forward durable gaps are repaired from the log (mutated)" FAIL \
  "$(verdict $PKG "$GAP_TESTS")"
revert internal/transport/sse/sse.go
report "forward durable gaps are repaired from the log (restored)" PASS \
  "$(verdict $PKG "$GAP_TESTS")"

echo "=== I. §13: a failed gap repair still writes the frame that exposed the hole ==="
snapshot internal/transport/sse/sse.go
python - <<'PYEOF'
import io
p = 'internal/transport/sse/sse.go'
s = io.open(p, encoding='utf-8').read()
old = (
    "\t\t\tif !ok {\n"
    "\t\t\t\t// Fail closed: end the transport and let the client\n"
    "\t\t\t\t// reconnect from its last contiguous cursor. Losing the\n"
    "\t\t\t\t// connection costs a reconnect; guessing here loses content.\n"
    "\t\t\t\treturn\n"
    "\t\t\t}\n"
)
assert old in s, "mutation I anchor missing"
new = (
    "\t\t\tif !ok {\n"
    "\t\t\t\t// FALSIFICATION: the repair failed, so fall back to what Redis\n"
    "\t\t\t\t// gave us — the hole is handed to the client.\n"
    "\t\t\t\tif !writeFrame(ev.Sequence, ev.EventType, ev.Payload, true) {\n"
    "\t\t\t\t\treturn\n"
    "\t\t\t\t}\n"
    "\t\t\t\tlastDelivered = ev.Sequence\n"
    "\t\t\t\tif ev.IsTerminal() {\n"
    "\t\t\t\t\treturn\n"
    "\t\t\t\t}\n"
    "\t\t\t\tcontinue\n"
    "\t\t\t}\n"
)
s = s.replace(old, new, 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "an unrepairable gap fails closed (mutated)" FAIL \
  "$(verdict $PKG TestGatewayFailsClosedWhenGapRepairFails)"
revert internal/transport/sse/sse.go
report "an unrepairable gap fails closed (restored)" PASS \
  "$(verdict $PKG TestGatewayFailsClosedWhenGapRepairFails)"

echo "=== J. §20: the byte bound goes back to being advisory ==="
snapshot internal/transport/sse/hub_cache.go
python - <<'PYEOF'
import io
p = 'internal/transport/sse/hub_cache.go'
s = io.open(p, encoding='utf-8').read()

# (1) an oversized event is retained instead of refused: the body of the
#     rejection is emptied, so control falls through to the append below.
old = (
    "\t\tevicted += len(r.buf) - r.head\n"
    "\t\tr.resetSegmentLocked()\n"
    "\t\treturn evicted, true\n"
    "\t}\n"
)
assert old in s, "mutation J anchor 1 missing"
new = (
    "\t\t// FALSIFICATION: retain it anyway — \"the newest event always\n"
    "\t\t// survives\", so the byte budget stops being a bound.\n"
    "\t}\n"
)
s = s.replace(old, new, 1)

# (2) eviction stops at one entry, so that event can never be dropped either.
old2 = "\t\tif live == 0 {\n\t\t\tbreak\n\t\t}\n"
assert old2 in s, "mutation J anchor 2 missing"
new2 = (
    "\t\t// FALSIFICATION: at least the newest entry survives, whatever its size.\n"
    "\t\tif live <= 1 {\n"
    "\t\t\tbreak\n"
    "\t\t}\n"
)
s = s.replace(old2, new2, 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "CacheMaxBytes is a hard bound (mutated)" FAIL \
  "$(verdict $PKG "$SIZE_TESTS")"
revert internal/transport/sse/hub_cache.go
report "CacheMaxBytes is a hard bound (restored)" PASS \
  "$(verdict $PKG "$SIZE_TESTS")"

echo "=== K. §25: the cache-metric generation fence is removed ==="
snapshot internal/transport/sse/hub.go
python - <<'PYEOF'
import io
p = 'internal/transport/sse/hub.go'
s = io.open(p, encoding='utf-8').read()
old = (
    "\tif m.hubs[runID] != hub {\n"
    "\t\t// Stale generation: this hub has been evicted or replaced.\n"
    "\t\treturn\n"
    "\t}\n"
)
assert old in s, "mutation K anchor missing"
new = (
    "\t// FALSIFICATION: no generation fence — a superseded hub can resurrect\n"
    "\t// its cache gauges for a run it no longer owns.\n"
)
s = s.replace(old, new, 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "cache metrics are fenced by hub generation (mutated)" FAIL \
  "$(verdict $PKG "$METRIC_TESTS")"
revert internal/transport/sse/hub.go
report "cache metrics are fenced by hub generation (restored)" PASS \
  "$(verdict $PKG "$METRIC_TESTS")"

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
