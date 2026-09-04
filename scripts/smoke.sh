#!/usr/bin/env bash
# bbs 冒烟测试:注册→建版块→发帖→回复→删除 + CSRF/权限负路径
# 用法: BASE=http://localhost:8090 ./scripts/smoke.sh
set -euo pipefail

BASE="${BASE:-http://localhost:8090}"
JAR="$(mktemp)"
CSRF=""
PASS=0; FAIL=0

ok()   { PASS=$((PASS+1)); echo "  ✓ $1"; }
bad()  { FAIL=$((FAIL+1)); echo "  ✗ $1"; }
check() { # check <描述> <期望> <实际>
  if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (期望 $2 实际 $3)"; fi
}
contains() { # contains <描述> <haystack> <needle>
  if echo "$2" | grep -q "$3"; then ok "$1"; else bad "$1 (未找到: $3)"; fi
}

csrf() { # 从页面表单里取 _csrf
  CSRF=$(curl -s -b "$JAR" -c "$JAR" "$1" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
}

echo "== 基础 =="
check "healthz" "ok" "$(curl -s "$BASE/healthz")"
HOME_HTML=$(curl -s -c "$JAR" "$BASE/")
contains "首页含种子版块「综合」" "$HOME_HTML" "综合"

echo "== 注册(首用户应为 admin)=="
csrf "$BASE/register"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -c "$JAR" \
  -d "_csrf=$CSRF&name=admin&password=password123" "$BASE/register")
check "注册跳转" "303" "$code"
HOME_HTML=$(curl -s -b "$JAR" "$BASE/")
contains "admin 登录态" "$HOME_HTML" "admin"
contains "admin 可见版块管理入口" "$HOME_HTML" "版块管理"

echo "== CSRF 负路径 =="
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" \
  -d "name=tech&slug=tech" "$BASE/admin/categories")
check "无 CSRF 建版块被拒" "403" "$code"

echo "== 建版块 + 发帖 =="
csrf "$BASE/"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" \
  -d "_csrf=$CSRF&name=技术分享&slug=tech&description=聊技术" "$BASE/admin/categories")
check "建版块" "303" "$code"

csrf "$BASE/c/tech/new"
REDIR=$(curl -s -o /dev/null -w '%{redirect_url}' -b "$JAR" \
  -d "_csrf=$CSRF&title=第一帖&content=大家好,这是正文" "$BASE/c/tech/new")
THREAD_URL="$REDIR"
contains "发帖后跳转到主题页" "$THREAD_URL" "/t/"
THREAD_HTML=$(curl -s -b "$JAR" "$THREAD_URL")
contains "主题页含标题" "$THREAD_HTML" "第一帖"
contains "主题页含正文" "$THREAD_HTML" "大家好"
contains "主题页含 op-card" "$THREAD_HTML" "op-card"
contains "主题页含版块徽章" "$THREAD_HTML" "bcat"

echo "== 首页帖子流 =="
FEED=$(curl -s -b "$JAR" "$BASE/")
contains "帖子流含最新主题" "$FEED" "第一帖"
contains "帖子流含分类徽章" "$FEED" "技术分享"
contains "帖子流含热帖 Tab" "$FEED" "热帖"
contains "顶栏含发帖入口" "$FEED" "/new"
HOT=$(curl -s -b "$JAR" "$BASE/?tab=hot")
contains "热帖页可访问" "$HOT" "热帖"
CATF=$(curl -s -b "$JAR" "$BASE/?cat=tech")
contains "分类筛选生效" "$CATF" "第一帖"
NPP=$(curl -s -b "$JAR" "$BASE/new")
contains "发帖选版块页" "$NPP" "技术分享"
contains "选版块直达编辑" "$NPP" "/c/tech/new"
code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/admin/categories")
check "未登录管理页被拒(跳登录)" "303" "$code"
code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/new")
check "未登录发帖页跳登录" "303" "$code"

echo "== 版块管理 =="
csrf "$BASE/admin/categories"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" \
  -d "_csrf=$CSRF&name=日常&slug=daily&description=随便聊聊" "$BASE/admin/categories")
check "后台建版块" "303" "$code"
ADMINPAGE=$(curl -s -b "$JAR" "$BASE/admin/categories")
contains "后台列表含新板块" "$ADMINPAGE" "日常"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" \
  -d "_csrf=$CSRF" "$BASE/admin/categories/3/delete")
check "删除空版块" "303" "$code"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" \
  -d "_csrf=$CSRF" "$BASE/admin/categories/2/delete")
check "删除非空版块被拒" "400" "$code"

echo "== 回复(htmx)==="
CSRF=$(curl -s -b "$JAR" "$THREAD_URL" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
REPLY_HTML=$(curl -s -b "$JAR" -H "X-CSRF-Token: $CSRF" \
  -d "content=这是第一条回复<script>alert(1)</script>" "$THREAD_URL/reply")
contains "回复渲染为 partial" "$REPLY_HTML" "这是第一条回复"
if echo "$REPLY_HTML" | grep -q "<script>"; then bad "XSS 未被转义"; else ok "XSS 被转义"; fi

THREAD_HTML=$(curl -s -b "$JAR" "$THREAD_URL")
contains "主题页现含回复" "$THREAD_HTML" "这是第一条回复"
contains "回复计数=1" "$THREAD_HTML" "1 回复"

echo "== 未登录负路径 =="
code=$(curl -s -o /dev/null -w '%{http_code}' \
  -d "content=hack" "$BASE/t/1/reply")
check "未登录回复被拒" "403" "$code"

echo "== 删除回复 =="
POST_ID=$(echo "$REPLY_HTML" | grep -o 'id="p[0-9]*"' | sed 's/id="p//;s/"//')
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -H "X-CSRF-Token: $CSRF" \
  -X POST "$BASE/p/$POST_ID/delete")
check "删除回复" "200" "$code"
THREAD_HTML=$(curl -s -b "$JAR" "$THREAD_URL")
if echo "$THREAD_HTML" | grep -q "这是第一条回复"; then bad "回复未被删除"; else ok "回复已删除"; fi

echo "== 第二个用户应为普通 user =="
JAR2=$(mktemp)
CSRF=$(curl -s -c "$JAR2" "$BASE/register" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
curl -s -o /dev/null -b "$JAR2" -c "$JAR2" -d "_csrf=$CSRF&name=bob&password=password456" "$BASE/register"
H=$(curl -s -b "$JAR2" "$BASE/")
if echo "$H" | grep -q "版块管理"; then bad "普通用户可见管理入口"; else ok "普通用户无管理入口"; fi
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR2" "$BASE/admin/categories")
check "普通用户管理页被拒" "403" "$code"

echo ""
echo "== Markdown 渲染(回复)=="
csrf "$THREAD_URL"
MD=$(curl -s -b "$JAR" -H "X-CSRF-Token: $CSRF" \
  -d "content=# 大标题%0A%0A- 甲%0A- 乙%0A%0A[坏链](javascript:alert(1))" "$THREAD_URL/reply")
contains "Markdown 标题渲染" "$MD" "<h1>大标题</h1>"
contains "Markdown 列表渲染" "$MD" "<li>甲</li>"
if echo "$MD" | grep -q "javascript:"; then bad "危险链接未被过滤"; else ok "危险链接被过滤"; fi
POST_ID=$(echo "$MD" | grep -o 'id="p[0-9]*"' | sed 's/id="p//;s/"//')

echo "== 编辑回复(作者本人)=="
csrf "$BASE/p/$POST_ID/edit"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" \
  -d "_csrf=$CSRF&content=改为加粗 **新内容**" "$BASE/p/$POST_ID/edit")
check "编辑回复跳转" "303" "$code"
THREAD_HTML=$(curl -s -b "$JAR" "$THREAD_URL")
contains "编辑后内容可见" "$THREAD_HTML" "新内容"
contains "已编辑标记" "$THREAD_HTML" "编辑于"
contains "Markdown 加粗渲染" "$THREAD_HTML" "<strong>新内容</strong>"

echo "== 编辑主题(管理员)=="
csrf "$BASE/t/1/edit"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" \
  -d "_csrf=$CSRF&title=第一帖(改)&content=大家好,这是**改后**正文" "$BASE/t/1/edit")
check "编辑主题跳转" "303" "$code"
THREAD_HTML=$(curl -s -b "$JAR" "$THREAD_URL")
contains "改后标题" "$THREAD_HTML" "第一帖(改)"
contains "首帖加粗渲染" "$THREAD_HTML" "<strong>改后</strong>"
CAT_HTML=$(curl -s -b "$JAR" "$BASE/c/tech")
contains "列表标题同步" "$CAT_HTML" "第一帖(改)"

echo "== 编辑权限负路径 =="
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR2" "$BASE/t/1/edit")
check "他人主题编辑页被拒" "403" "$code"
csrf "$BASE/"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR2" \
  -d "_csrf=$CSRF&content=劫持" "$BASE/p/$POST_ID/edit")
check "他人回复编辑被拒" "403" "$code"
echo "== 资料与头像 =="
csrf "$BASE/u/1"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" "$BASE/u/1")
check "资料页可访问" "200" "$code"
P=$(curl -s -b "$JAR" "$BASE/u/1")
contains "资料页含用户名" "$P" "admin"
contains "资料页含管理员徽章" "$P" "管理员"
contains "资料页含资料区" "$P" "member-top"
contains "资料页含统计chips" "$P" "member-chips"
contains "资料页含成员序号" "$P" "位成员"
contains "资料页含TA的主题" "$P" "TA 的主题"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR2" "$BASE/u/1/edit")
check "他人资料编辑页被拒" "403" "$code"

AV=$(mktemp)
printf 'GIF89a\001\000\001\000\200\000\000\377\377\377\000\000\000' > "$AV"
UPJSON=$(curl -s -b "$JAR" -H "X-CSRF-Token: $CSRF" -F "file=@$AV;type=image/gif" "$BASE/uploads")
UURL=$(echo "$UPJSON" | sed -n 's/.*"url":"\([^"]*\)".*/\1/p')
contains "上传返回 URL" "$UURL" "/uploads/"
code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE$UURL")
check "上传文件可访问" "200" "$code"
CT=$(curl -s -o /dev/null -w '%{content_type}' "$BASE$UURL")
check "上传文件类型为 gif" "image/gif" "$CT"

code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" \
  -F "_csrf=$CSRF" -F "bio=折腾 VPS 与自托管" -F "avatar=@$AV;type=image/gif" "$BASE/u/1/edit")
check "保存资料跳转" "303" "$code"
P=$(curl -s -b "$JAR" "$BASE/u/1")
contains "简介已保存" "$P" "折腾 VPS 与自托管"
AVURL=$(echo "$P" | grep -o 'src="/uploads/[0-9]*"' | head -1 | sed 's/src="//;s/"//')
contains "头像已展示" "$P" "$AVURL"
THREAD_HTML=$(curl -s -b "$JAR" "$THREAD_URL")
contains "帖子头像已替换" "$THREAD_HTML" "$AVURL"
UP=$(curl -s -b "$JAR" "$BASE/c/tech/new")
contains "发帖编辑器带上传入口" "$UP" "data-upload"
TR=$(curl -s -b "$JAR" "$THREAD_URL")
contains "回复表单带上传入口" "$TR" "data-upload"
IMG=$(curl -s -b "$JAR" -H "X-CSRF-Token: $CSRF" \
  -d "content=看图 ![图]($AVURL)" "$THREAD_URL/reply")
contains "Markdown 图片渲染" "$IMG" "<img src=\"$AVURL\" alt=\"图\">"
echo "== 版主操作 =="
CSRF=$(curl -s -b "$JAR2" -c "$JAR2" "$BASE/" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR2" \
  -H "X-CSRF-Token: $CSRF" -X POST "$BASE/t/1/pin")
check "普通用户置顶被拒" "403" "$code"
csrf "$BASE/u/2"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" \
  -d "_csrf=$CSRF&role=mod" "$BASE/admin/users/2/role")
check "提升为版主" "303" "$code"
CSRF=$(curl -s -b "$JAR2" -c "$JAR2" "$BASE/t/1" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR2" \
  -d "_csrf=$CSRF" "$BASE/t/1/lock")
check "版主锁帖" "303" "$code"
T=$(curl -s -b "$JAR2" "$BASE/t/1")
contains "锁定徽章显示" "$T" "badge-lock"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR2" \
  -d "_csrf=$CSRF&content=锁帖后回复" "$BASE/t/1/reply")
check "锁帖后回复被拒" "403" "$code"
CSRF=$(curl -s -b "$JAR2" -c "$JAR2" "$BASE/t/1" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR2" \
  -d "_csrf=$CSRF" "$BASE/t/1/lock")
check "版主解锁" "303" "$code"
T=$(curl -s -b "$JAR2" "$BASE/t/1")
if echo "$T" | grep -q "badge-lock"; then bad "解锁后徽章仍在"; else ok "解锁后徽章消失"; fi

echo "== 封禁与解封 =="
csrf "$BASE/u/2"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" \
  -d "_csrf=$CSRF&days=1" "$BASE/admin/users/2/ban")
check "封禁用户" "303" "$code"
H=$(curl -s -b "$JAR2" "$BASE/")
if echo "$H" | grep -q "nav-user"; then bad "封禁后会话仍有效"; else ok "封禁后会话失效"; fi
CSRF=$(curl -s -b "$JAR2" -c "$JAR2" "$BASE/login" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
LOGIN=$(curl -s -b "$JAR2" -c "$JAR2" \
  -d "_csrf=$CSRF&name=bob&password=password456" "$BASE/login")
contains "封禁提示" "$LOGIN" "已被封禁"
csrf "$BASE/u/2"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" \
  -d "_csrf=$CSRF" "$BASE/admin/users/2/unban")
check "解除封禁" "303" "$code"
H=$(curl -s -b "$JAR2" "$BASE/")
if echo "$H" | grep -q "nav-user"; then ok "解封后会话恢复"; else bad "解封后会话未恢复"; fi

echo "== 上传负路径 =="
JAR3=$(mktemp)
CSRF=$(curl -s -c "$JAR3" "$BASE/login" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR3" \
  -H "X-CSRF-Token: $CSRF" -F "file=@$AV;type=image/gif" "$BASE/uploads")
check "匿名上传被拒(跳转登录)" "303" "$code"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR3" \
  -H "X-CSRF-Token: $CSRF" -F "file=@$AV;type=text/plain" "$BASE/uploads")
check "未登录非图片负路径一致性" "303" "$code"
echo "== 通知 =="
csrf "$BASE/c/tech/new"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" \
  -d "_csrf=$CSRF&title=通知测试&content=欢迎 @bob 来逛" "$BASE/c/tech/new")
check "发 @bob 主题" "303" "$code"
UN=$(curl -s -b "$JAR2" "$BASE/notifications/unread")
if echo "$UN" | grep -q '"unread":[1-9]'; then ok "bob 收到 mention 未读"; else bad "bob 未收到 mention ($UN)"; fi
NP=$(curl -s -b "$JAR2" "$BASE/notifications")
contains "通知页含 mention" "$NP" "@了你"
contains "通知页标出未读" "$NP" "未读"
UN=$(curl -s -b "$JAR2" "$BASE/notifications/unread")
if echo "$UN" | grep -q '"unread":[1-9]'; then ok "打开列表不清除未读"; else bad "不应自动清零 ($UN)"; fi
CSRF=$(curl -s -b "$JAR2" -c "$JAR2" "$BASE/notifications" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR2" \
  -d "_csrf=$CSRF" "$BASE/notifications/read-all")
check "全部已读提交" "303" "$code"
UN=$(curl -s -b "$JAR2" "$BASE/notifications/unread")
check "全部已读后清零" '{"unread":0}' "$UN"

CSRF=$(curl -s -b "$JAR2" -c "$JAR2" "$BASE/t/1" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
curl -s -o /dev/null -b "$JAR2" -d "_csrf=$CSRF&content=收到" "$BASE/t/1/reply"
UN=$(curl -s -b "$JAR" "$BASE/notifications/unread")
if echo "$UN" | grep -q '"unread":[1-9]'; then ok "admin 收到回复未读"; else bad "admin 未收到回复 ($UN)"; fi
NP=$(curl -s -b "$JAR" "$BASE/notifications")
contains "admin 通知页含回复" "$NP" "回复了你的主题"
NID=$(echo "$NP" | grep -o 'data-nid="[0-9]*"' | head -1 | sed 's/.*="//;s/"//')
CSRF=$(curl -s -b "$JAR" -c "$JAR" "$BASE/notifications" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" \
  -H "X-CSRF-Token: $CSRF" -X POST "$BASE/notifications/$NID/read")
check "单条已读提交" "204" "$code"
csrf "$BASE/notifications"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" \
  -d "_csrf=$CSRF" "$BASE/notifications/read-all")
check "admin 全部已读提交" "303" "$code"
UN=$(curl -s -b "$JAR" "$BASE/notifications/unread")
check "admin 通知已清零" '{"unread":0}' "$UN"

UN=$(curl -s "$BASE/notifications/unread")
check "匿名未读为 0" '{"unread":0}' "$UN"
echo "== 搜索 =="
code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/search")
check "搜索页可访问" "200" "$code"
S=$(curl -s --get --data-urlencode "q=大家好" "$BASE/search")
contains "正文命中主题" "$S" "第一帖(改)"
S2=$(curl -s --get --data-urlencode "q=新内容" "$BASE/search")
contains "回复命中主题" "$S2" "第一帖(改)"
S3=$(curl -s --get --data-urlencode "q=完全不存在的词xyz" "$BASE/search")
contains "无结果提示" "$S3" "没有找到"
S4=$(curl -s --get --data-urlencode "q=第一帖(改)" "$BASE/search")
contains "FTS 命中编辑后标题" "$S4" "第一帖(改)"
S5=$(curl -s --get --data-urlencode "q=第一" "$BASE/search")
contains "短查询回退 LIKE" "$S5" "第一帖(改)"
code=$(curl -s -o /dev/null -w '%{http_code}' --get --data-urlencode "q=大家好" "$BASE/search")
check "带参搜索 200" "200" "$code"
rm -f "$AV"
echo "结果: $PASS 通过, $FAIL 失败"
[ "$FAIL" -eq 0 ] && echo "SMOKE OK" || exit 1
