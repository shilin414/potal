#!/usr/bin/env bash
# Falsification driver — 第九轮「补丁批次 3.3.1」(复审报告 §三十五).
#
# Three mutations, one per 3.3.1 fix; each must make its matching test FAIL, then
# PASS again after the fix is restored:
#
#   1. restore the history-driven capacity SQL
#        (runs STRAIGHT_JOIN provider_submissions → provider_submissions JOIN runs)
#        → TestProviderCapacityQueryIsDrivenByActiveRuns FAILs
#          (provider_submissions becomes the driving leg, and neither table is
#           reached through an index that starts at status / run_id)
#   2. echo the raw stream_protocol request instead of negotiating it
#        → TestSSEStreamProtocolMetricCardinalityIsBounded FAILs
#          (every distinct positive integer mints a Prometheus time series)
#   3. drop one active status from a capacity query's IN list
#        → TestProviderCapacityStatusListIsExactComplementOfSettled FAILs
#          (the list must stay the exact complement of IsSettled)
#
# The pre-existing 3.3 driver (scripts/falsify_review9_patch33.sh) is kept intact
# and is NOT replaced by this one: 3.3.1 adds guards, it does not retract any.
#
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

run_pkg() { # package pattern
  STUDIO_TEST_DB=1 STUDIO_TEST_REDIS=1 go test "$1" \
    -run "$2" -count=1 2>&1 | grep -E "^(--- FAIL|--- PASS|FAIL|ok)" | head -5
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

# The remote leg of the non-excluding capacity query is the one
# TestProviderCapacityQueryIsDrivenByActiveRuns EXPLAINs, so that is the leg
# mutation 1 reverts. The excluding (admission-decision) leg is left alone: it
# is exercised by the same query shape and would only blur which change caused
# the plan regression.
echo "=== 1. §十/§十一: the capacity query goes back to history-driven ==="
# Mutating the SQL TEXT the driver sends is what "drives from the ledger" means;
# the checked-in generated code is that text, and re-running sqlc would produce
# exactly this file back on revert anyway.
snapshot internal/gen/db/execution.sql.go
python - <<'PYEOF'
import io
p = 'internal/gen/db/execution.sql.go'
s = io.open(p, encoding='utf-8').read()
old = (
    "    SELECT r.id AS run_id\n"
    "    FROM runs r\n"
    "    STRAIGHT_JOIN provider_submissions ps\n"
    "      ON ps.run_id = r.id\n"
    "     AND ps.provider = r.provider\n"
    "    WHERE r.provider = ?\n"
    "      AND r.status IN (\n"
    "          'queued',\n"
    "          'running',\n"
    "          'waiting_input',\n"
    "          'waiting_external',\n"
    "          'cancelling'\n"
    "      )\n"
    "      AND ps.state IN ('sending', 'unknown', 'accepted')\n"
    ") capacity_runs\n"
)
assert old in s, "mutation 1 anchor missing"
new = (
    "    SELECT ps.run_id\n"
    "    FROM provider_submissions ps\n"
    "    JOIN runs r ON r.id = ps.run_id\n"
    "    WHERE ps.provider = ?\n"
    "      AND ps.state IN ('sending', 'unknown', 'accepted')\n"
    "      AND r.status NOT IN (\n"
    "          'cancelled',\n"
    "          'succeeded',\n"
    "          'failed',\n"
    "          'interrupted'\n"
    "      )\n"
    ") capacity_runs\n"
)
s = s.replace(old, new, 1)  # FALSIFICATION: history-driven remote leg
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "capacity plan is driven by active runs (mutated)" FAIL \
  "$(verdict ./tests/integration/ 'TestProviderCapacityQueryIsDrivenByActiveRuns')"
revert internal/gen/db/execution.sql.go
report "capacity plan is driven by active runs (restored)" PASS \
  "$(verdict ./tests/integration/ 'TestProviderCapacityQueryIsDrivenByActiveRuns')"

echo "=== 2. §二十四: stream_protocol echoes the request instead of negotiating ==="
snapshot internal/transport/sse/sse.go
python - <<'PYEOF'
import io
p = 'internal/transport/sse/sse.go'
s = io.open(p, encoding='utf-8').read()
old = (
    "\trequested, err := strconv.Atoi(raw)\n"
    "\tif err != nil || requested < StreamProtocolRangeDelta {\n"
    "\t\treturn StreamProtocolLegacy\n"
    "\t}\n"
    "\treturn StreamProtocolRangeDelta\n"
)
assert old in s, "mutation 2 anchor missing"
new = (
    "\trequested, err := strconv.Atoi(raw)\n"
    "\tif err != nil || requested < StreamProtocolLegacy {\n"
    "\t\treturn StreamProtocolLegacy\n"
    "\t}\n"
    "\treturn requested // FALSIFICATION: echo the raw request value\n"
)
s = s.replace(old, new, 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "Prometheus protocol label stays bounded (mutated)" FAIL \
  "$(verdict ./internal/transport/sse/ 'TestSSEStreamProtocolMetricCardinalityIsBounded')"
report "future protocol versions clamp down to 2 (mutated)" FAIL \
  "$(verdict ./internal/transport/sse/ 'TestStreamProtocolNegotiatesFutureVersionsDown')"
revert internal/transport/sse/sse.go
report "Prometheus protocol label stays bounded (restored)" PASS \
  "$(verdict ./internal/transport/sse/ 'TestSSEStreamProtocolMetricCardinalityIsBounded')"
report "future protocol versions clamp down to 2 (restored)" PASS \
  "$(verdict ./internal/transport/sse/ 'TestStreamProtocolNegotiatesFutureVersionsDown')"

echo "=== 3. §九: an active status drops out of the capacity IN list ==="
# The guard reads db/queries/execution.sql, not the generated copy, so the
# source is what has to be mutated: the point of the guard is to catch the
# QUERY FILE drifting away from the status enum before sqlc is ever re-run.
snapshot db/queries/execution.sql
python - <<'PYEOF'
import io
p = 'db/queries/execution.sql'
s = io.open(p, encoding='utf-8').read()
marker = '-- name: CountProviderEffectiveInflight :one'
start = s.index(marker)
end = s.index('-- name: ', start + len(marker))
block = s[start:end]
assert "          'cancelling'\n" in block, "mutation 3 anchor missing"
block = block.replace("          'cancelling'\n", "", 1)  # FALSIFICATION
s = s[:start] + block + s[end:]
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "capacity status list == complement of IsSettled (mutated)" FAIL \
  "$(verdict ./tests/integration/ 'TestProviderCapacityStatusListIsExactComplementOfSettled')"
revert db/queries/execution.sql
report "capacity status list == complement of IsSettled (restored)" PASS \
  "$(verdict ./tests/integration/ 'TestProviderCapacityStatusListIsExactComplementOfSettled')"

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
