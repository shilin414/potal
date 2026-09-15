#!/usr/bin/env bash
# Falsification driver — 第九轮「补丁批次 3.2-A」前端 mutation (复审报告 §十七 #1).
#
#   1. 去掉 transient delta 的 offset 对账（delta 恢复为无条件 append）
#      → reverse-overlap（chunk 先到、buffered delta 后到）测试必须 FAIL。
set -u
export PATH="/usr/bin:/bin:/c/software/Git/cmd:$PATH"
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

run_test() { # vitest title filter
  npx vitest run src/stores/__tests__/useRunChatStore.test.ts -t "$1" 2>&1 \
    | grep -E "Tests  |failed|passed" | head -3
}

verdict() { # title → FAIL | PASS
  local out
  out="$(run_test "$1")"
  if echo "$out" | grep -q "failed"; then echo FAIL; else echo PASS; fi
}

cp src/stores/useRunChatStore.ts src/stores/useRunChatStore.ts.orig
cleanup() {
  for f in $(find src -name '*.orig' 2>/dev/null); do
    mv "$f" "${f%.orig}"
    echo "  [restored] ${f%.orig} (interrupted falsification)"
  done
}
trap cleanup EXIT

echo "=== 复审 §二/§四: transient deltas ignore the shared byte offset again ==="
python - <<'PYEOF'
import io
p = 'src/stores/useRunChatStore.ts'
s = io.open(p, encoding='utf-8').read()
old = '        if (payload.offset !== undefined) {'
new = '        if (false) { // FALSIFICATION: deltas append unconditionally again'
assert old in s, "mutation 1 anchor missing"
s = s.replace(old, new, 1)
io.open(p, 'w', encoding='utf-8', newline='\n').write(s)
PYEOF
report "reverse-order chunk→buffered delta dedupes (mutated)" FAIL "$(verdict 'reverse order')"
report "legacy deltas still append (mutated)" PASS "$(verdict 'legacy delta')"
mv src/stores/useRunChatStore.ts.orig src/stores/useRunChatStore.ts
report "reverse-order chunk→buffered delta dedupes (restored)" PASS "$(verdict 'reverse order')"

echo
echo "=== leftovers (must be empty) ==="
find src -name '*.orig' -print
echo
echo "frontend falsifications ok=$pass bad=$fail"
[ "$fail" -eq 0 ] || exit 1
