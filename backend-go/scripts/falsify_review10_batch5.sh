#!/usr/bin/env bash
# Falsification driver — 第十轮 Batch 5「Worker Dispatcher」(执行文档 §38).
#
# Seven mutations, one per fail-closed defence the batch claims to have
# closed; each must make its matching test FAIL, then PASS again after the
# code is restored:
#
#   A. the (provider, runtime_type) pair degrades to a single hardcoded
#      route — runtime_type stops participating in routing (§34)
#        → TestRuntimeTypeSelectsDifferentHandlers FAIL
#   B. the route key is read from the FROZEN SNAPSHOT instead of the
#      canonical runs column (§13/§14)
#        → TestCanonicalColumnsWinOverSnapshot FAIL
#   C. a missing runtime route FALLS BACK to the agent executor (§16)
#        → TestMissingRuntimeFailsClosed FAIL
#   D. an unknown provider resolves to feishu_aily anyway (§26/§34)
#        → TestUnknownProviderCannotResolve FAIL
#   E. the snapshot mismatch guards are deleted outright (§14)
#        → TestCanonicalColumnsWinOverSnapshot FAIL
#      (B and E fail the same test through DIFFERENT defects: B routes on
#       the snapshot and lands in route_missing; E keeps the canonical
#       lookup and executes the diverging run.)
#   H. ONLY the snapshot provider_key guard is deleted, runtime guard intact
#      (Batch 5.1, P2-3/P2-4)
#        → TestSnapshotProviderMismatchFailsClosed FAIL
#      (the provider half of the guard gets its own deterministic test, so
#       deleting it alone can no longer hide from every unit test and
#       mutation)
#   F. duplicate provider registration becomes last-write-wins (§9.7)
#        → TestDuplicateProviderRegistrationRejected FAIL
#   G. a ProviderSlots wired to another provider is accepted (§9.6)
#        → TestProviderSlotMustBelongToProvider FAIL
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

PKG=./internal/workerdispatch/
SRC=internal/workerdispatch/dispatcher.go
PAIR_TESTS='TestRuntimeTypeSelectsDifferentHandlers'
SNAP_TESTS='TestCanonicalColumnsWinOverSnapshot'
PROV_SNAP_TESTS='TestSnapshotProviderMismatchFailsClosed'
MISSING_TESTS='TestMissingRuntimeFailsClosed'
UNKNOWN_TESTS='TestUnknownProviderCannotResolve'
DUP_TESTS='TestDuplicateProviderRegistrationRejected'
SLOTS_TESTS='TestProviderSlotMustBelongToProvider'

echo "=== A. §34: routing degrades to one hardcoded route (no runtime_type) ==="
snapshot "$SRC"
python - <<'PYEOF'
import io
p = 'internal/workerdispatch/dispatcher.go'
s = io.open(p, encoding='utf-8').read()
old = "\thandler, ok := d.routes[run.RuntimeType]\n"
assert s.count(old) == 1, "mutation A anchor missing or ambiguous"
new = "\thandler, ok := d.routes[catalog.RuntimeTypeAgent] // FALSIFICATION\n"
s = s.replace(old, new, 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "runtime_type selects the handler (mutated)" FAIL \
  "$(verdict $PKG "$PAIR_TESTS")"
revert "$SRC"
report "runtime_type selects the handler (restored)" PASS \
  "$(verdict $PKG "$PAIR_TESTS")"

echo "=== B. §13: the route key is read from the frozen snapshot ==="
snapshot "$SRC"
python - <<'PYEOF'
import io
p = 'internal/workerdispatch/dispatcher.go'
s = io.open(p, encoding='utf-8').read()

# (1) drop the runtime_type snapshot guard — the mutated router TRUSTS the
# snapshot, so guarding against it contradicts the mutation.
old = (
    "\tif snapRuntime := run.SnapshotString(\"runtime_type\"); snapRuntime != \"\" && snapRuntime != run.RuntimeType {\n"
    "\t\td.observe(run, DispatchSnapshotMismatch)\n"
    "\t\td.log.Error(\"worker runtime route snapshot mismatch\",\n"
    "\t\t\t\"provider\", run.Provider,\n"
    "\t\t\t\"runtime_type\", run.RuntimeType,\n"
    "\t\t\t\"snapshot_runtime_type\", snapRuntime)\n"
    "\t\treturn d.failTerminal(ctx, claimed, ErrCodeRuntimeRouteSnapshotMismatch,\n"
    "\t\t\tfmt.Sprintf(\"runtime snapshot runtime_type %q diverges from canonical runs.runtime_type %q\",\n"
    "\t\t\t\tsnapRuntime, run.RuntimeType))\n"
    "\t}\n"
)
assert s.count(old) == 1, "mutation B anchor 1 (runtime guard) missing"
s = s.replace(old, "", 1)

# (2) route on the snapshot value, falling back to the column only when the
# snapshot lacks the field.
old2 = "\thandler, ok := d.routes[run.RuntimeType]\n"
assert s.count(old2) == 1, "mutation B anchor 2 (lookup) missing"
new2 = (
    "\truntimeKey := run.SnapshotString(\"runtime_type\") // FALSIFICATION\n"
    "\tif runtimeKey == \"\" {\n"
    "\t\truntimeKey = run.RuntimeType\n"
    "\t}\n"
    "\thandler, ok := d.routes[runtimeKey]\n"
)
s = s.replace(old2, new2, 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "canonical columns win over the snapshot (mutated)" FAIL \
  "$(verdict $PKG "$SNAP_TESTS")"
revert "$SRC"
report "canonical columns win over the snapshot (restored)" PASS \
  "$(verdict $PKG "$SNAP_TESTS")"

echo "=== C. §16: a missing route falls back to the agent executor ==="
snapshot "$SRC"
python - <<'PYEOF'
import io
p = 'internal/workerdispatch/dispatcher.go'
s = io.open(p, encoding='utf-8').read()
old = (
    "\thandler, ok := d.routes[run.RuntimeType]\n"
    "\tif !ok || handler == nil {\n"
)
assert s.count(old) == 1, "mutation C anchor missing or ambiguous"
new = (
    "\thandler, ok := d.routes[run.RuntimeType]\n"
    "\tif !ok || handler == nil {\n"
    "\t\thandler, ok = d.routes[catalog.RuntimeTypeAgent] // FALSIFICATION\n"
    "\t}\n"
    "\tif !ok || handler == nil {\n"
)
s = s.replace(old, new, 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "a missing runtime fails closed (mutated)" FAIL \
  "$(verdict $PKG "$MISSING_TESTS")"
revert "$SRC"
report "a missing runtime fails closed (restored)" PASS \
  "$(verdict $PKG "$MISSING_TESTS")"

echo "=== D. §26: an unknown provider resolves to feishu_aily anyway ==="
snapshot "$SRC"
python - <<'PYEOF'
import io
p = 'internal/workerdispatch/dispatcher.go'
s = io.open(p, encoding='utf-8').read()
old = (
    "\tif !ok {\n"
    "\t\treturn nil, fmt.Errorf(\"%w: %q\", ErrUnknownProvider, provider)\n"
    "\t}\n"
)
assert s.count(old) == 1, "mutation D anchor missing or ambiguous"
new = (
    "\tif !ok {\n"
    "\t\tentry, ok = r.providers[\"feishu_aily\"] // FALSIFICATION\n"
    "\t\tif !ok {\n"
    "\t\t\treturn nil, fmt.Errorf(\"%w: %q\", ErrUnknownProvider, provider)\n"
    "\t\t}\n"
    "\t}\n"
)
s = s.replace(old, new, 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "an unknown provider cannot resolve (mutated)" FAIL \
  "$(verdict $PKG "$UNKNOWN_TESTS")"
revert "$SRC"
report "an unknown provider cannot resolve (restored)" PASS \
  "$(verdict $PKG "$UNKNOWN_TESTS")"

echo "=== E. §14: the snapshot mismatch guard is deleted outright ==="
snapshot "$SRC"
python - <<'PYEOF'
import io
p = 'internal/workerdispatch/dispatcher.go'
s = io.open(p, encoding='utf-8').read()

old_provider = (
    "\tif snapProvider := run.SnapshotString(\"provider_key\"); snapProvider != \"\" && snapProvider != run.Provider {\n"
    "\t\td.observe(run, DispatchSnapshotMismatch)\n"
    "\t\td.log.Error(\"worker runtime route snapshot mismatch\",\n"
    "\t\t\t\"provider\", run.Provider,\n"
    "\t\t\t\"runtime_type\", run.RuntimeType,\n"
    "\t\t\t\"snapshot_provider\", snapProvider)\n"
    "\t\treturn d.failTerminal(ctx, claimed, ErrCodeRuntimeRouteSnapshotMismatch,\n"
    "\t\t\tfmt.Sprintf(\"runtime snapshot provider_key %q diverges from canonical runs.provider %q\",\n"
    "\t\t\t\tsnapProvider, run.Provider))\n"
    "\t}\n"
)
assert s.count(old_provider) == 1, "mutation E anchor 1 (provider guard) missing"
s = s.replace(old_provider, "", 1)

old_runtime = (
    "\tif snapRuntime := run.SnapshotString(\"runtime_type\"); snapRuntime != \"\" && snapRuntime != run.RuntimeType {\n"
    "\t\td.observe(run, DispatchSnapshotMismatch)\n"
    "\t\td.log.Error(\"worker runtime route snapshot mismatch\",\n"
    "\t\t\t\"provider\", run.Provider,\n"
    "\t\t\t\"runtime_type\", run.RuntimeType,\n"
    "\t\t\t\"snapshot_runtime_type\", snapRuntime)\n"
    "\t\treturn d.failTerminal(ctx, claimed, ErrCodeRuntimeRouteSnapshotMismatch,\n"
    "\t\t\tfmt.Sprintf(\"runtime snapshot runtime_type %q diverges from canonical runs.runtime_type %q\",\n"
    "\t\t\t\tsnapRuntime, run.RuntimeType))\n"
    "\t}\n"
)
assert s.count(old_runtime) == 1, "mutation E anchor 2 (runtime guard) missing"
s = s.replace(old_runtime, "", 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "the snapshot guard fails closed (mutated)" FAIL \
  "$(verdict $PKG "$SNAP_TESTS")"
revert "$SRC"
report "the snapshot guard fails closed (restored)" PASS \
  "$(verdict $PKG "$SNAP_TESTS")"

echo "=== H. Batch 5.1 P2-3: ONLY the snapshot provider_key guard is deleted ==="
snapshot "$SRC"
python - <<'PYEOF'
import io
p = 'internal/workerdispatch/dispatcher.go'
s = io.open(p, encoding='utf-8').read()
old = (
    "\tif snapProvider := run.SnapshotString(\"provider_key\"); snapProvider != \"\" && snapProvider != run.Provider {\n"
    "\t\td.observe(run, DispatchSnapshotMismatch)\n"
    "\t\td.log.Error(\"worker runtime route snapshot mismatch\",\n"
    "\t\t\t\"provider\", run.Provider,\n"
    "\t\t\t\"runtime_type\", run.RuntimeType,\n"
    "\t\t\t\"snapshot_provider\", snapProvider)\n"
    "\t\treturn d.failTerminal(ctx, claimed, ErrCodeRuntimeRouteSnapshotMismatch,\n"
    "\t\t\tfmt.Sprintf(\"runtime snapshot provider_key %q diverges from canonical runs.provider %q\",\n"
    "\t\t\t\tsnapProvider, run.Provider))\n"
    "\t}\n"
)
assert s.count(old) == 1, "mutation H anchor missing or ambiguous"
s = s.replace(old, "", 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "the provider snapshot guard fails closed (mutated)" FAIL \
  "$(verdict $PKG "$PROV_SNAP_TESTS")"
revert "$SRC"
report "the provider snapshot guard fails closed (restored)" PASS \
  "$(verdict $PKG "$PROV_SNAP_TESTS")"

echo "=== F. §9.7: duplicate provider registration becomes last-write-wins ==="
snapshot "$SRC"
python - <<'PYEOF'
import io
p = 'internal/workerdispatch/dispatcher.go'
s = io.open(p, encoding='utf-8').read()
old = (
    "\tif _, exists := r.providers[spec.Key]; exists {\n"
    "\t\treturn fmt.Errorf(\"%w: %q\", ErrDuplicateProvider, spec.Key)\n"
    "\t}\n"
)
assert s.count(old) == 1, "mutation F anchor missing or ambiguous"
s = s.replace(old, "", 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "duplicate registration is rejected (mutated)" FAIL \
  "$(verdict $PKG "$DUP_TESTS")"
revert "$SRC"
report "duplicate registration is rejected (restored)" PASS \
  "$(verdict $PKG "$DUP_TESTS")"

echo "=== G. §9.6: ProviderSlots wired to another provider are accepted ==="
snapshot "$SRC"
python - <<'PYEOF'
import io
p = 'internal/workerdispatch/dispatcher.go'
s = io.open(p, encoding='utf-8').read()
old = (
    "\tif spec.Slots != nil && spec.Slots.Provider != spec.Key {\n"
    "\t\treturn fmt.Errorf(\"%w: spec key %q, slots provider %q\",\n"
    "\t\t\tErrProviderSlotMismatch, spec.Key, spec.Slots.Provider)\n"
    "\t}\n"
)
assert s.count(old) == 1, "mutation G anchor missing or ambiguous"
s = s.replace(old, "", 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "foreign ProviderSlots are rejected (mutated)" FAIL \
  "$(verdict $PKG "$SLOTS_TESTS")"
revert "$SRC"
report "foreign ProviderSlots are rejected (restored)" PASS \
  "$(verdict $PKG "$SLOTS_TESTS")"

echo "=== control: the one registered route still dispatches normally ==="
report "a registered runtime is dispatched end to end" PASS \
  "$(verdict $PKG TestRegisteredRuntimeIsDispatched)"

echo
echo "=== leftovers (all must be empty) ==="
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
