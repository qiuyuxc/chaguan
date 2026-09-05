#!/usr/bin/env bash
# 邮件链路端到端测试:开启邮件注册 → 注册发验证信 → 点链接激活 → 忘记密码 → 重置。
# 需要 python3(用 scripts/fakesmtp.py 起一个假 SMTP 收信),不依赖真实邮件服务商。
# 用法: bash scripts/mailflow.sh            (自动挑端口起一个临时实例)
#       BASE=... SMTP_PORT=... bash scripts/mailflow.sh   (指定端口)
# 注意:这里刻意不开 pipefail —— 脚本里大量 `echo 大段内容 | grep -q` 与
# `grep | head` 的写法,前者会让 grep 命中即退出、把 echo 打成 SIGPIPE,
# 后者同理;开 pipefail 会把这些正常情况判成失败(表现为断言假失败或脚本中断)。
set -eu

PORT="${PORT:-8181}"
SMTP_PORT="${SMTP_PORT:-2531}"
BASE="http://localhost:$PORT"
BIN="${BIN:-./bbs}"
PASS=0; FAIL=0
ok()   { PASS=$((PASS+1)); echo "  ✓ $1"; }
bad()  { FAIL=$((FAIL+1)); echo "  ✗ $1"; }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (期望 $2 实际 $3)"; fi; }
has()  { if echo "$2" | grep -q "$3"; then ok "$1"; else bad "$1 (未找到: $3)"; fi; }

MAILBOX=$(mktemp); WORK=$(mktemp -d)
cleanup() { kill ${SP:-0} ${SMTP:-0} 2>/dev/null || true; rm -rf "$WORK" "$MAILBOX" "$JA" "$JB" "$JC" "$JD" 2>/dev/null || true; }
trap cleanup EXIT

[ -x "$BIN" ] || { echo "先编译: go build -o bbs ./cmd/bbs"; exit 1; }
python3 scripts/fakesmtp.py "$SMTP_PORT" "$MAILBOX" >/dev/null 2>&1 & SMTP=$!
PORT=$PORT BBS_DB=$WORK/t.db BBS_UPLOADS=$WORK "$BIN" >"$WORK/log" 2>&1 & SP=$!
for _ in $(seq 1 30); do [ "$(curl -s -m 2 $BASE/healthz)" = "ok" ] && break; sleep 0.5; done

tok() { curl -s -b "$1" -c "$1" "$2" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//'; }
JA=$(mktemp); JB=$(mktemp); JC=$(mktemp); JD=$(mktemp)

echo "== 准备:管理员 + 打开邮件注册 =="
T=$(curl -s -c "$JA" $BASE/register | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
curl -s -o /dev/null -b "$JA" -c "$JA" -d "_csrf=$T&name=admin&password=admin12345" $BASE/register
T=$(tok "$JA" $BASE/admin/mail)
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JA" -d "_csrf=$T" \
  --data-urlencode "host=127.0.0.1" --data-urlencode "port=$SMTP_PORT" --data-urlencode "secure=none" \
  --data-urlencode "from=bbs@example.com" --data-urlencode "email_register=1" $BASE/admin/mail)
check "保存 SMTP 并开启邮件注册" "303" "$code"
has "注册页出现邮箱字段" "$(curl -s $BASE/register)" 'name="email"'

echo "== 注册 → 验证邮件 =="
T=$(curl -s -c "$JB" $BASE/register | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
R=$(curl -s -b "$JB" -c "$JB" -d "_csrf=$T&name=mailuser&password=password123&email=mailuser%40example.com" $BASE/register)
has "注册后提示去邮箱" "$R" "去邮箱收一下"
check "注册后未直接登录" "0" "$(curl -s -b "$JB" $BASE/ | grep -c nav-user)"
T=$(tok "$JB" $BASE/login)
has "未验证登录被拦" "$(curl -s -b "$JB" -d "_csrf=$T&name=mailuser&password=password123" $BASE/login)" "邮箱还没验证"
sleep 0.5
VT=$(grep -aoE 'verify/email\?token=[a-f0-9]+' "$MAILBOX" | head -1 | sed 's/.*token=//')
check "收到验证令牌" "64" "${#VT}"
check "点验证链接跳登录" "303" "$(curl -s -o /dev/null -w '%{http_code}' "$BASE/verify/email?token=$VT")"
T=$(curl -s -c "$JC" $BASE/login | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
curl -s -o /dev/null -b "$JC" -c "$JC" -d "_csrf=$T&name=mailuser&password=password123" $BASE/login
check "验证后可以登录" "1" "$(curl -s -b "$JC" $BASE/ | grep -c nav-user)"
has "验证链接不可复用" "$(curl -s "$BASE/verify/email?token=$VT")" "链接无效或已过期"

echo "== 忘记密码 → 重置 =="
T=$(tok "$JD" $BASE/forgot)
curl -s -o /dev/null -b "$JD" -c "$JD" -d "_csrf=$T&email=mailuser%40example.com" $BASE/forgot
sleep 0.5
RT=$(grep -aoE 'reset\?token=[a-f0-9]+' "$MAILBOX" | tail -1 | sed 's/.*token=//')
check "收到重置令牌" "64" "${#RT}"
T=$(tok "$JD" "$BASE/reset?token=$RT")
check "提交新密码" "303" "$(curl -s -o /dev/null -w '%{http_code}' -b "$JD" \
  -d "_csrf=$T&token=$RT&password=newpass12345&password2=newpass12345" $BASE/reset)"
JE=$(mktemp)
T=$(curl -s -c "$JE" $BASE/login | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
curl -s -o /dev/null -b "$JE" -c "$JE" -d "_csrf=$T&name=mailuser&password=newpass12345" $BASE/login
check "新密码可登录" "1" "$(curl -s -b "$JE" $BASE/ | grep -c nav-user)"
check "重置后旧会话失效" "0" "$(curl -s -b "$JC" $BASE/ | grep -c nav-user)"
has "重置链接不可复用" "$(curl -s "$BASE/reset?token=$RT")" "链接无效"
rm -f "$JE"

echo "== 收信统计 =="
check "共收到 2 封邮件" "2" "$(grep -ac 'MAIL-END' "$MAILBOX")"
has "主题按 RFC 2047 编码" "$(grep -a '^Subject' "$MAILBOX" | head -1)" "=?utf-8?q?"

echo ""
echo "结果: $PASS 通过, $FAIL 失败"
[ "$FAIL" -eq 0 ] && echo "MAILFLOW OK" || exit 1
