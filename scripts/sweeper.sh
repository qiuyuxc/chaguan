#!/usr/bin/env bash
# 后台巡检两件事:红包超时自动退回、抽奖定点开奖。
# 开一个 TTL=2s / 巡检=1s 的实例,把生产的「24 小时」压成两秒。
# 定点开奖那段没法压 —— datetime-local 只能精确到分钟,所以会等到下一个整分。
# 用法: bash scripts/sweeper.sh
# 注意:这里刻意不开 pipefail —— 脚本里大量 `echo 大段内容 | grep -q` 与
# `grep | head` 的写法,前者会让 grep 命中即退出、把 echo 打成 SIGPIPE,
# 后者同理;开 pipefail 会把这些正常情况判成失败。
set -eu

PORT="${PORT:-8183}"
BASE="http://localhost:$PORT"
BIN="${BIN:-./chaguan}"
PASS=0; FAIL=0
ok()   { PASS=$((PASS+1)); echo "  ✓ $1"; }
bad()  { FAIL=$((FAIL+1)); echo "  ✗ $1"; }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (期望 $2 实际 $3)"; fi; }
has()  { if grep -q -- "$3" <<<"$2"; then ok "$1"; else bad "$1 (未找到: $3)"; fi; }
lacks(){ if grep -q -- "$3" <<<"$2"; then bad "$1 (不该出现: $3)"; else ok "$1"; fi; }

WORK=$(mktemp -d); JA=$(mktemp); JB=$(mktemp)
cleanup() { kill ${SP:-0} 2>/dev/null || true; rm -rf "$WORK" "$JA" "$JB" 2>/dev/null || true; }
trap cleanup EXIT

[ -x "$BIN" ] || { echo "先编译: go build -o chaguan ./cmd/chaguan"; exit 1; }
# TZ 钉死成 UTC:datetime-local 不带时区,服务端按 time.Local 解析,脚本里的 date 用
# 系统时区。两边不一致时「下一个整分」会算错,定点开奖那段永远等不到。
export TZ=UTC
PORT=$PORT CHAGUAN_DB=$WORK/t.db CHAGUAN_UPLOADS=$WORK CHAGUAN_RP_TTL=2s CHAGUAN_SWEEP=1s \
  "$BIN" >"$WORK/log" 2>&1 & SP=$!
for _ in $(seq 1 30); do [ "$(curl -s -m 2 $BASE/healthz)" = "ok" ] && break; sleep 0.5; done

tok() { curl -s -b "$1" -c "$1" "$2" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//'; }
bal() { curl -s -b "$1" $BASE/points | grep -oE '<b class="pt-num">[0-9]+' | sed 's/.*>//'; }

echo "== 准备两个账号 =="
T=$(tok "$JA" $BASE/register)
curl -s -o /dev/null -b "$JA" -c "$JA" -d "_csrf=$T&name=rpsender&password=password123" $BASE/register
T=$(tok "$JB" $BASE/register)
curl -s -o /dev/null -b "$JB" -c "$JB" -d "_csrf=$T&name=rptaker&password=password123" $BASE/register
check "两个账号都登录了" "2" "$(( $(curl -s -b "$JA" $BASE/ | grep -c nav-user) + $(curl -s -b "$JB" $BASE/ | grep -c nav-user) ))"

# 首个注册用户是管理员,这里 rpsender 就是 admin,可以给自己加积分
T=$(tok "$JA" $BASE/admin/points)
curl -s -o /dev/null -b "$JA" -d "_csrf=$T&delta=100&note=红包超时测试" $BASE/admin/points/1/adjust
check "发送者有 100 积分" "100" "$(bal "$JA")"

echo "== 发一个红包,不领 =="
T=$(tok "$JA" $BASE/u/2)
DMID=$(curl -s -o /dev/null -w '%{redirect_url}' -b "$JA" -d "_csrf=$T&to=2" $BASE/messages/start | grep -oE '[0-9]+$')
# 先发一条普通消息,免得撤回/退回时把整条会话删掉(那是「发错人」的路径)
T=$(tok "$JA" "$BASE/messages/$DMID")
curl -s -o /dev/null -b "$JA" -H "X-CSRF-Token: $T" --data-urlencode "body=先聊一句" "$BASE/messages/$DMID/send"
RP=$(curl -s -b "$JA" -H "X-CSRF-Token: $T" -d "amount=30" "$BASE/messages/$DMID/redpack")
has "红包发出" "$RP" "等待对方领取"
check "发出即扣款" "70" "$(bal "$JA")"
has "收件人看到红包" "$(curl -s -b "$JB" "$BASE/messages/$DMID")" "对方给你发了个红包"

echo "== 等巡检把它退回 =="
for _ in $(seq 1 20); do
  grep -q "红包超时退回" "$WORK/log" && break
  sleep 0.5
done
has "日志记下超时退回" "$(cat "$WORK/log")" "红包超时退回"
check "积分退回发送者" "100" "$(bal "$JA")"
has "流水记超时退回" "$(curl -s -b "$JA" $BASE/points)" "超时退回"
D=$(curl -s -b "$JB" "$BASE/messages/$DMID")
has "气泡改成已超时退回" "$D" "已超时退回"
has "超时退回仍显示金额" "$D" "30 积分"
lacks "超时不该当成主动撤回" "$D" "撤回了一条消息"
lacks "退回后没有领取按钮了" "$D" ">领取<"

echo "== 已领取的不该被巡检动 =="
T=$(tok "$JA" "$BASE/messages/$DMID")
RP2=$(curl -s -b "$JA" -H "X-CSRF-Token: $T" -d "amount=20" "$BASE/messages/$DMID/redpack")
MSG=$(echo "$RP2" | grep -oE 'name="msg" value="[0-9]+"' | tail -1 | grep -oE '[0-9]+' || true)
T=$(tok "$JB" "$BASE/messages/$DMID")
CL=$(curl -s -b "$JB" -H "X-CSRF-Token: $T" -d "msg=$MSG" "$BASE/messages/$DMID/claim")
has "对方及时领到" "$CL" "已领取"
B=$(bal "$JB")
sleep 3
check "领过的红包不会再被退回" "$B" "$(bal "$JB")"
check "发送者余额也不受影响" "80" "$(bal "$JA")"
has "领取状态没被巡检改掉" "$(curl -s -b "$JB" "$BASE/messages/$DMID")" "已领取"

echo "== 抽奖定点开奖 =="
# datetime-local 只到分钟,所以把 draw_at 设到下一个整分再等过去。
# 快到整分时先跨过去,免得刚提交就已经过期(校验要求晚于现在)。
LEFT=$(( 60 - 10#$(date +%S) ))   # 10# 强制十进制:秒数 08/09 会被当八进制报错
[ "$LEFT" -lt 6 ] && { sleep "$((LEFT + 1))"; LEFT=60; }
WHEN=$(date -d "@$(( $(date +%s) + LEFT ))" +%Y-%m-%dT%H:%M 2>/dev/null || date -v+${LEFT}S +%Y-%m-%dT%H:%M)
T=$(tok "$JA" $BASE/admin/points)
curl -s -o /dev/null -b "$JA" -d "_csrf=$T&delta=200&note=定点开奖测试" $BASE/admin/points/1/adjust
T=$(tok "$JA" $BASE/new)
LID=$(curl -s -o /dev/null -w '%{redirect_url}' -b "$JA" -d "_csrf=$T" \
  --data-urlencode "category=general" --data-urlencode "title=定点开奖测试" \
  --data-urlencode "content=到点自动开" --data-urlencode "kind=lottery_points" \
  --data-urlencode "sponsor=12" --data-urlencode "winners=0" --data-urlencode "stake=0" \
  --data-urlencode "draw_at=$WHEN" $BASE/new | grep -oE '[0-9]+$')
check "发出定点开奖帖" "1" "$([ -n "$LID" ] && echo 1 || echo 0)"
L=$(curl -s -b "$JA" "$BASE/t/$LID")
has "卡片写明自动开奖时间" "$L" "自动开奖"
T=$(tok "$JB" "$BASE/t/$LID")
curl -s -o /dev/null -b "$JB" -H "X-CSRF-Token: $T" -d "content=我来参与" "$BASE/t/$LID/reply"
has "参与成功" "$(curl -s -b "$JB" "$BASE/t/$LID")" "你已参与"
echo "  … 等下一个整分($WHEN)自动开奖,最多 ${LEFT} 秒"
for _ in $(seq 1 80); do
  grep -q "定点开奖 主题 $LID" "$WORK/log" && break
  sleep 1
done
has "日志记下定点开奖" "$(cat "$WORK/log")" "定点开奖 主题 $LID"
L=$(curl -s -b "$JA" "$BASE/t/$LID")
has "帖子转成已开奖" "$L" "已开奖"
WON=$(grep -oE 'lot-win">中奖 \+[0-9]+' <<<"$L" | grep -oE '[0-9]+$')
check "奖池全给了唯一参与者" "12" "$(awk '{s+=$1} END{print s+0}' <<<"$WON")"
has "中奖者收到积分" "$(curl -s -b "$JB" $BASE/points)" "抽奖中奖"
has "中奖者收到通知" "$(curl -s -b "$JB" $BASE/notifications)" "你中奖了"

echo
echo "结果: $PASS 通过, $FAIL 失败"
[ "$FAIL" -eq 0 ] || exit 1
echo "SWEEPER OK"
