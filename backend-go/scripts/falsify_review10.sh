#!/usr/bin/env bash
# Falsification driver — 第十轮 Batch 4「SSE Hub」(执行报告 §54).
#
# Seven mutations, one per invariant the batch claims; each must make its
# matching test FAIL, then PASS again after the code is restored:
#
#   A. GetOrCreate mints a hub per call        → one Redis subscription per VIEWER
#        → TestHubManagerSharesOneUpstreamPerRun FAILs
#   B. the transient-delta capability filter is disabled
#        → TestHubSubscriberProtocolIsolation FAILs
#      (a legacy client would receive a delta it cannot reconcile)
#   C. the fan-out offers become BLOCKING      → one slow browser stalls the run
#        → TestHubFanoutIsNonBlocking FAILs
#   D. the subscriber is registered AFTER the replay
#        → TestGatewayRegistersSubscriberBeforeReplay /
#          TestGatewayBuffersTransientDeltaUntilCatchUpCompletes FAIL
#      (events published during the replay reach nobody)
#   E. the hub runtime context inherits the startup context
#        → TestHubRuntimeContextSurvivesStartupContextExpiry FAILs
#      (every stream would die 30s into the process)
#   F. a sequence gap still claims cache continuity
#        → TestDurableRingGapInvalidatesContinuity /
#          TestGatewayCacheGapFallsBackToMySQL /
#          TestHubCacheGapFallsBackToMySQL FAIL
#      (the client loses every event inside the gap while its cursor advances)
#   G. the terminal event stops being a hard boundary (both guards removed)
#        → TestHubTerminalIsAHardBoundary FAILs
#
# Mutation G changes TWO lines on purpose: the boundary is implemented twice
# (dispatch refuses to fan out after a terminal; the upstream loop stops
# reading), and each guard alone hides the other. "Terminal is not a boundary"
# is the one defect, so it is one mutation.
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

echo "=== A. §5.1: GetOrCreate mints a new hub per call ==="
# Batch 4.1 (§28) turned GetOrCreate into a loop, so the mutation is now "the
# registry lookup returns nothing": same defect, one line, and the loop's
# creation branch still runs — which is what mints a hub (and a Redis
# subscription) per request.
snapshot internal/transport/sse/hub.go
python - <<'PYEOF'
import io
p = 'internal/transport/sse/hub.go'
s = io.open(p, encoding='utf-8').read()
old = "\t\thub := m.hubs[runID]\n\t\tif hub == nil {\n"
assert old in s, "mutation A anchor missing"
new = (
    "\t\t// FALSIFICATION: no registry lookup — every request builds its own hub,\n"
    "\t\t// i.e. one Redis subscription per SSE connection.\n"
    "\t\thub := (*RunHub)(nil)\n"
    "\t\tif hub == nil {\n"
)
s = s.replace(old, new, 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "one run shares one upstream (mutated)" FAIL \
  "$(verdict $PKG TestHubManagerSharesOneUpstreamPerRun)"
revert internal/transport/sse/hub.go
report "one run shares one upstream (restored)" PASS \
  "$(verdict $PKG TestHubManagerSharesOneUpstreamPerRun)"

echo "=== B. §16: the transient-delta capability filter is disabled ==="
snapshot internal/transport/sse/hub_subscription.go
python - <<'PYEOF'
import io
p = 'internal/transport/sse/hub_subscription.go'
s = io.open(p, encoding='utf-8').read()
old = (
    "\tif ev.Sequence == 0 && ev.EventType == execution.EventContentDelta &&\n"
    "\t\ts.protocol < StreamProtocolRangeDelta {\n"
    "\t\treturn false\n"
    "\t}\n"
)
assert old in s, "mutation B anchor missing"
new = (
    "\t// FALSIFICATION: every subscriber accepts every frame, whatever it\n"
    "\t// negotiated — a legacy client would render \"ABCABC\".\n"
    "\tif ev.Sequence == 0 && ev.EventType == execution.EventContentDelta &&\n"
    "\t\ts.protocol < 0 {\n"
    "\t\treturn false\n"
    "\t}\n"
)
s = s.replace(old, new, 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "protocol 1 never receives a transient delta (mutated)" FAIL \
  "$(verdict $PKG TestHubSubscriberProtocolIsolation)"
revert internal/transport/sse/hub_subscription.go
report "protocol 1 never receives a transient delta (restored)" PASS \
  "$(verdict $PKG TestHubSubscriberProtocolIsolation)"

echo "=== C. §17: the fan-out offer becomes a blocking send ==="
snapshot internal/transport/sse/hub_subscription.go
python - <<'PYEOF'
import io
p = 'internal/transport/sse/hub_subscription.go'
s = io.open(p, encoding='utf-8').read()
old = (
    "\tselect {\n"
    "\tcase s.events <- ev:\n"
    "\t\treturn \"\"\n"
    "\tdefault:\n"
    "\t\tif s.maxBytes > 0 {\n"
    "\t\t\ts.queuedBytes.Add(-int64(ev.ApproxBytes))\n"
    "\t\t}\n"
    "\t\treturn DropReasonSlowConsumer\n"
    "\t}\n"
)
assert old in s, "mutation C anchor missing"
new = (
    "\t// FALSIFICATION: blocking broadcast — one browser that stops reading\n"
    "\t// stalls the hub loop, every other viewer, and the Redis channel.\n"
    "\ts.events <- ev\n"
    "\treturn \"\"\n"
)
s = s.replace(old, new, 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "one slow subscriber cannot stall the hub (mutated)" FAIL \
  "$(verdict $PKG TestHubFanoutIsNonBlocking)"
revert internal/transport/sse/hub_subscription.go
report "one slow subscriber cannot stall the hub (restored)" PASS \
  "$(verdict $PKG TestHubFanoutIsNonBlocking)"

echo "=== D. §9: replay first, register second ==="
# The ordering is expressed by WHERE attachHub is called, so the mutation moves
# the call below the replay — a text MOVE, not a rewrite, so the mutated code
# stays compilable and the failure is the real one.
snapshot internal/transport/sse/sse.go
python - <<'PYEOF'
import io
p = 'internal/transport/sse/sse.go'
s = io.open(p, encoding='utf-8').read()
register = (
    "\thub, sub := g.attachHub(ctx, run, protocol)\n"
    "\tif sub != nil {\n"
    "\t\tdefer sub.Close(DropReasonClientGone)\n"
    "\t}\n"
)
assert register in s, "mutation D anchor missing"
s = s.replace(register, "", 1)
late = "\tif replayedTerminal {\n"
assert late in s, "mutation D insertion point missing"
s = s.replace(late, "\t// FALSIFICATION: registered only after the replay.\n" + register + late, 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "subscriber registered before the replay (mutated)" FAIL \
  "$(verdict $PKG 'TestGatewayRegistersSubscriberBeforeReplay|TestGatewayBuffersTransientDeltaUntilCatchUpCompletes')"
revert internal/transport/sse/sse.go
report "subscriber registered before the replay (restored)" PASS \
  "$(verdict $PKG 'TestGatewayRegistersSubscriberBeforeReplay|TestGatewayBuffersTransientDeltaUntilCatchUpCompletes')"

echo "=== E. §6: the hub runtime context inherits the startup context ==="
snapshot internal/transport/sse/hub.go
python - <<'PYEOF'
import io
p = 'internal/transport/sse/hub.go'
s = io.open(p, encoding='utf-8').read()
old = (
    "\t_ = startupCtx\n"
    "\tif upstream == nil {\n"
    "\t\tupstream = unavailableUpstream{}\n"
    "\t}\n"
    "\truntimeCtx, cancel := context.WithCancel(context.Background())\n"
)
assert old in s, "mutation E anchor missing"
new = (
    "\tif upstream == nil {\n"
    "\t\tupstream = unavailableUpstream{}\n"
    "\t}\n"
    "\tif startupCtx == nil {\n"
    "\t\tstartupCtx = context.Background()\n"
    "\t}\n"
    "\t// FALSIFICATION: the hub runtime dies with app.Build's 30s context.\n"
    "\truntimeCtx, cancel := context.WithCancel(startupCtx)\n"
)
s = s.replace(old, new, 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "hub lifecycle is independent of the startup context (mutated)" FAIL \
  "$(verdict $PKG TestHubRuntimeContextSurvivesStartupContextExpiry)"
revert internal/transport/sse/hub.go
report "hub lifecycle is independent of the startup context (restored)" PASS \
  "$(verdict $PKG TestHubRuntimeContextSurvivesStartupContextExpiry)"

echo "=== F. §11.1/§42: a sequence gap still claims cache continuity ==="
# Batch 4.1 (§20) re-expressed the gap reset as resetSegmentLocked() and split
# "what the ring has seen" from "what it retains"; the anchor below moved with
# it. The defect is unchanged: the segment keeps extending through a hole.
snapshot internal/transport/sse/hub_cache.go
python - <<'PYEOF'
import io
p = 'internal/transport/sse/hub_cache.go'
s = io.open(p, encoding='utf-8').read()
old = (
    "\t\tevicted = live\n"
    "\t\tr.resetSegmentLocked()\n"
    "\t\tdiscontinuous = true\n"
)
assert old in s, "mutation F anchor missing"
new = (
    "\t\t// FALSIFICATION: extend through the gap, silently claiming to\n"
    "\t\t// cover the sequences that were never seen.\n"
    "\t\tevicted = 0\n"
)
s = s.replace(old, new, 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "cache fails closed on a gap (mutated)" FAIL \
  "$(verdict $PKG 'TestDurableRingGapInvalidatesContinuity|TestGatewayCacheGapFallsBackToMySQL|TestHubCacheGapFallsBackToMySQL')"
revert internal/transport/sse/hub_cache.go
report "cache fails closed on a gap (restored)" PASS \
  "$(verdict $PKG 'TestDurableRingGapInvalidatesContinuity|TestGatewayCacheGapFallsBackToMySQL|TestHubCacheGapFallsBackToMySQL')"

echo "=== G. §19: the terminal event is no longer a hard boundary ==="
snapshot internal/transport/sse/hub.go
python - <<'PYEOF'
import io
p = 'internal/transport/sse/hub.go'
s = io.open(p, encoding='utf-8').read()
guard = "if h.closed || h.terminal {\n\t\th.mu.Unlock()\n\t\treturn\n\t}\n"
assert guard in s, "mutation G anchor 1 missing"
s = s.replace(guard, "// FALSIFICATION: dirty events after a terminal are fanned out.\n\tif h.closed {\n\t\th.mu.Unlock()\n\t\treturn\n\t}\n", 1)
loop = (
    "\t\t\th.dispatch(ev)\n"
    "\t\t\tif h.isTerminal() {\n"
    "\t\t\t\treturn\n"
    "\t\t\t}\n"
)
assert loop in s, "mutation G anchor 2 missing"
s = s.replace(loop, "\t\t\t// FALSIFICATION: keep reading after the terminal.\n\t\t\th.dispatch(ev)\n", 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "terminal is a hard boundary (mutated)" FAIL \
  "$(verdict $PKG TestHubTerminalIsAHardBoundary)"
revert internal/transport/sse/hub.go
report "terminal is a hard boundary (restored)" PASS \
  "$(verdict $PKG TestHubTerminalIsAHardBoundary)"

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
