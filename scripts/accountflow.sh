#!/usr/bin/env bash
# 两步验证(TOTP)端到端:生成密钥 → 开启 → 登录要验证码 → 关闭。
# 需要 python3 算 TOTP(和验证器同一套算法),不依赖任何第三方库。
# 用法: bash scripts/accountflow.sh
# 注意:这里刻意不开 pipefail —— 脚本里大量 `echo 大段内容 | grep -q` 与
# `grep | head` 的写法,前者会让 grep 命中即退出、把 echo 打成 SIGPIPE,
# 后者同理;开 pipefail 会把这些正常情况判成失败(表现为断言假失败或脚本中断)。
set -eu

PORT="${PORT:-8182}"
BASE="http://localhost:$PORT"
BIN="${BIN:-./bbs}"
PASS=0; FAIL=0
ok()   { PASS=$((PASS+1)); echo "  ✓ $1"; }
bad()  { FAIL=$((FAIL+1)); echo "  ✗ $1"; }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (期望 $2 实际 $3)"; fi; }
has()  { if echo "$2" | grep -q "$3"; then ok "$1"; else bad "$1 (未找到: $3)"; fi; }

WORK=$(mktemp -d); JA=$(mktemp); JB=$(mktemp)
cleanup() { kill ${SP:-0} 2>/dev/null || true; rm -rf "$WORK" "$JA" "$JB" 2>/dev/null || true; }
trap cleanup EXIT

[ -x "$BIN" ] || { echo "先编译: go build -o bbs ./cmd/bbs"; exit 1; }
PORT=$PORT BBS_DB=$WORK/t.db BBS_UPLOADS=$WORK "$BIN" >"$WORK/log" 2>&1 & SP=$!
for _ in $(seq 1 30); do [ "$(curl -s -m 2 $BASE/healthz)" = "ok" ] && break; sleep 0.5; done

tok() { curl -s -b "$1" -c "$1" "$2" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//'; }
# totp <base32secret> —— 与 internal/auth 的实现对齐(HMAC-SHA1 / 6 位 / 30 秒)
totp() {
  python3 - "$1" <<'PY'
import base64, hashlib, hmac, struct, sys, time
secret = sys.argv[1].strip().upper()
secret += "=" * (-len(secret) % 8)
key = base64.b32decode(secret)
counter = int(time.time()) // 30
digest = hmac.new(key, struct.pack(">Q", counter), hashlib.sha1).digest()
off = digest[-1] & 0x0F
code = (struct.unpack(">I", digest[off:off+4])[0] & 0x7FFFFFFF) % 10**6
print(f"{code:06d}")
PY
}

echo "== 准备账号 =="
T=$(curl -s -c "$JA" $BASE/register | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
curl -s -o /dev/null -b "$JA" -c "$JA" -d "_csrf=$T&name=tfauser&password=password123" $BASE/register
check "注册并登录" "1" "$(curl -s -b "$JA" $BASE/ | grep -c nav-user)"

echo "== 开启两步验证 =="
T=$(tok "$JA" $BASE/account)
SETUP=$(curl -s -b "$JA" -d "_csrf=$T" $BASE/account/2fa/setup)
has "生成密钥后给出手动添加信息" "$SETUP" "tfa-key"
SECRET=$(echo "$SETUP" | grep -oE '<code class="tfa-key">[A-Z2-7]+' | sed 's/.*>//')
check "密钥长度 32" "32" "${#SECRET}"
has "给出 otpauth 链接" "$SETUP" "otpauth://totp/"
T=$(tok "$JA" $BASE/account)
BADCODE=$(curl -s -b "$JA" -d "_csrf=$T&code=000000" $BASE/account/2fa/enable)
has "错误验证码不能开启" "$BADCODE" "验证码不对"
T=$(tok "$JA" $BASE/account)
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JA" -d "_csrf=$T&code=$(totp "$SECRET")" $BASE/account/2fa/enable)
check "正确验证码开启成功" "303" "$code"
has "账号页显示已开启" "$(curl -s -b "$JA" $BASE/account)" "已开启"

echo "== 登录要过两步验证 =="
T=$(curl -s -c "$JB" $BASE/login | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
LOC=$(curl -s -o /dev/null -w '%{redirect_url}' -b "$JB" -c "$JB" \
  -d "_csrf=$T&name=tfauser&password=password123" $BASE/login)
has "密码正确后跳到两步验证" "$LOC" "/login/2fa"
check "此时还没有登录态" "0" "$(curl -s -b "$JB" $BASE/ | grep -c nav-user)"
has "两步验证页可访问" "$(curl -s -b "$JB" $BASE/login/2fa)" "两步验证"
T=$(tok "$JB" $BASE/login/2fa)
W=$(curl -s -b "$JB" -d "_csrf=$T&code=000000" $BASE/login/2fa)
has "错误验证码被拒" "$W" "验证码不对"
check "被拒后仍未登录" "0" "$(curl -s -b "$JB" $BASE/ | grep -c nav-user)"
T=$(tok "$JB" $BASE/login/2fa)
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JB" -c "$JB" -d "_csrf=$T&code=$(totp "$SECRET")" $BASE/login/2fa)
check "正确验证码放行" "303" "$code"
check "拿到登录态" "1" "$(curl -s -b "$JB" $BASE/ | grep -c nav-user)"

echo "== 关闭两步验证 =="
T=$(tok "$JB" $BASE/account)
W=$(curl -s -b "$JB" -d "_csrf=$T&code=000000" $BASE/account/2fa/disable)
has "错误验证码不能关闭" "$W" "无法关闭"
T=$(tok "$JB" $BASE/account)
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JB" -d "_csrf=$T&code=$(totp "$SECRET")" $BASE/account/2fa/disable)
check "正确验证码关闭成功" "303" "$code"
has "账号页回到未开启" "$(curl -s -b "$JB" $BASE/account)" "生成密钥"
JC=$(mktemp)
T=$(curl -s -c "$JC" $BASE/login | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
curl -s -o /dev/null -b "$JC" -c "$JC" -d "_csrf=$T&name=tfauser&password=password123" $BASE/login
check "关闭后密码直接登录" "1" "$(curl -s -b "$JC" $BASE/ | grep -c nav-user)"
rm -f "$JC"

echo ""
echo "结果: $PASS 通过, $FAIL 失败"
[ "$FAIL" -eq 0 ] && echo "ACCOUNTFLOW OK" || exit 1
