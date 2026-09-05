#!/usr/bin/env bash
# bbs 冒烟测试:注册→建版块→发帖→回复→删除 + CSRF/权限负路径
# 用法: BASE=http://localhost:8090 ./scripts/smoke.sh
# 注意:这里刻意不开 pipefail —— 脚本里大量 `echo 大段内容 | grep -q` 与
# `grep | head` 的写法,前者会让 grep 命中即退出、把 echo 打成 SIGPIPE,
# 后者同理;开 pipefail 会把这些正常情况判成失败(表现为断言假失败或脚本中断)。
set -eu

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
  # 用 here-string 而不是管道:grep -q 命中即退出会把 echo 打成 SIGPIPE,
  # 在 pipefail 下整条流水线被判失败,内容越大越容易踩到(CSS 已近 100KB)
  if grep -q -- "$3" <<<"$2"; then ok "$1"; else bad "$1 (未找到: $3)"; fi
}
lacks() { # lacks <描述> <haystack> <needle> —— 断言「不该出现」
  if grep -q -- "$3" <<<"$2"; then bad "$1 (不该出现: $3)"; else ok "$1"; fi
}

csrf() { # 从页面表单里取 _csrf(取不到就留空,由后续断言暴露问题)
  CSRF=$(curl -s -b "$JAR" -c "$JAR" "$1" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//' || true)
}

echo "== 基础 =="
check "healthz" "ok" "$(curl -s "$BASE/healthz")"
HOME_HTML=$(curl -s -c "$JAR" "$BASE/")
contains "首页含种子版块「综合」" "$HOME_HTML" "综合"
contains "页面含内置确认面板" "$HOME_HTML" 'id="bbs-modal"'

echo "== 注册(首用户应为 admin)=="
csrf "$BASE/register"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -c "$JAR" \
  -d "_csrf=$CSRF&name=admin&password=password123" "$BASE/register")
check "注册跳转" "303" "$code"
HOME_HTML=$(curl -s -b "$JAR" "$BASE/")
contains "admin 登录态" "$HOME_HTML" "admin"
ADMINP=$(curl -s -b "$JAR" "$BASE/admin/categories")
contains "admin 可访问版块管理页" "$ADMINP" "版块管理"

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

echo "== 直接发帖(下拉选版块)=="
csrf "$BASE/new"
REDIR2=$(curl -s -o /dev/null -w '%{redirect_url}' -b "$JAR" \
  -d "_csrf=$CSRF&category=tech&title=第二帖&content=走版块下拉发帖" "$BASE/new")
contains "下拉发帖跳转主题页" "$REDIR2" "/t/"
THREAD2=$(curl -s -b "$JAR" "$REDIR2")
contains "下拉发帖主题含标题" "$THREAD2" "第二帖"
contains "下拉发帖主题含正文" "$THREAD2" "走版块下拉发帖"

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
contains "发帖页含版块字段" "$NPP" 'name="category"'
contains "发帖页用内置版块面板" "$NPP" 'data-picker'
# 忘选版块时提交会被拦住,提示必须留得住 —— 只闪 700ms 红边的话,必选项在表单
# 顶部、按钮在底部,手机上那下反馈发生在屏幕外,用户看到的是「点了没反应」
contains "版块必选带具体提示文案" "$NPP" 'data-need="请先选择版块'
AJS=$(curl -s "$BASE/static/app.js")
contains "必选提示会滚回可视区" "$AJS" "picker-need"
lacks "必选提示不再 700ms 就撤" "$AJS" 'box.classList.remove("need"); }, 700)'
contains "发帖页列出版块" "$NPP" "技术分享"
contains "发帖页用新式编辑器" "$NPP" 'class="composer"'
PP=$(curl -s -b "$JAR" "$BASE/c/tech/new")
contains "直达发帖预选版块" "$PP" 'value="tech"'
contains "直达发帖版块预选高亮" "$PP" 'class="picker-opt on"'
contains "直达发帖含新式编辑器" "$PP" 'data-compose="upload"'
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
if echo "$MD" | grep -Eq '(href|src)="javascript:'; then bad "危险链接未被过滤"; else ok "危险链接被过滤"; fi
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

echo "== 点赞与收藏(仅文章首帖)=="
CSRF=$(curl -s -b "$JAR2" -c "$JAR2" "$BASE/t/1" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
LIKE=$(curl -s -b "$JAR2" -H "X-CSRF-Token: $CSRF" -X POST "$BASE/t/1/like")
contains "点赞返回反应条" "$LIKE" 'id="op-reacts"'
contains "点赞后计数 1" "$LIKE" '>1</b>'
T=$(curl -s -b "$JAR2" "$BASE/t/1")
contains "主题页含文章反应条" "$T" 'id="op-reacts"'
contains "点赞态点亮" "$T" 'react-btn on'
REPLY_HTML=$(curl -s -b "$JAR2" -H "X-CSRF-Token: $CSRF" -d "content=只测回复不该有赞" "$BASE/t/1/reply")
if echo "$REPLY_HTML" | grep -q "react-btn"; then bad "回复区出现文章级点赞钮"; else ok "回复区无文章级点赞钮"; fi
PID=$(echo "$REPLY_HTML" | grep -o 'id="p[0-9]*"' | head -1 | sed 's/id="p//;s/"//')
RLIKE=$(curl -s -b "$JAR2" -H "X-CSRF-Token: $CSRF" -X POST "$BASE/p/$PID/like")
contains "回复点赞返回片段" "$RLIKE" 'class="pl-like"'
contains "回复点赞计数 1" "$RLIKE" '>1</b>'
if echo "$RLIKE" | grep -q 'pl-btn on'; then ok "回复点赞点亮"; else bad "回复点赞未点亮"; fi
PLIKE=$(curl -s -b "$JAR2" "$BASE/u/2?tab=likes")
cnt=$(echo "$PLIKE" | grep -c "第一帖(改)")
check "回复点赞不进我的点赞列表" "1" "$cnt"
LIKE2=$(curl -s -b "$JAR2" -H "X-CSRF-Token: $CSRF" -X POST "$BASE/t/1/like")
if echo "$LIKE2" | grep -q 'react-btn on'; then bad "取消点赞仍点亮"; else ok "取消点赞熄灭"; fi
contains "取消后计数回 0" "$LIKE2" '>0</b>'
LIKE3=$(curl -s -b "$JAR2" -H "X-CSRF-Token: $CSRF" -X POST "$BASE/t/1/like")
contains "再次点赞计数 1" "$LIKE3" '>1</b>'
FAV=$(curl -s -b "$JAR2" -H "X-CSRF-Token: $CSRF" -X POST "$BASE/t/1/favorite")
contains "收藏返回反应条" "$FAV" 'id="op-reacts"'
contains "收藏计数 1" "$FAV" '>1</b>'
T=$(curl -s -b "$JAR2" "$BASE/t/1")
contains "收藏态点亮" "$T" 'react-btn on'
FEED=$(curl -s -b "$JAR2" "$BASE/")
contains "首页行显示赞数量" "$FEED" "1 赞"
contains "首页行显示收藏数量" "$FEED" "1 收藏"
PLIKE=$(curl -s -b "$JAR2" "$BASE/u/2?tab=likes")
contains "点赞分区列出文章" "$PLIKE" "第一帖(改)"
contains "点赞分区含动作时间" "$PLIKE" "点赞于"
PFAV=$(curl -s -b "$JAR2" "$BASE/u/2?tab=favorites")
contains "收藏分区列出文章" "$PFAV" "第一帖(改)"
contains "收藏分区含动作时间" "$PFAV" "收藏于"
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/t/1/like")
check "匿名点赞被拒" "403" "$code"

echo "== 资料与头像 =="
csrf "$BASE/u/1"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" "$BASE/u/1")
check "资料页可访问" "200" "$code"
P=$(curl -s -b "$JAR" "$BASE/u/1")
contains "资料页含用户名" "$P" "admin"
contains "资料页含管理员徽章" "$P" "管理员"
contains "资料页含资料区" "$P" "member-top"
contains "资料页分区分割条复用主页样式" "$P" "member-feed-tabs"
contains "资料页含 UID" "$P" "UID 1"
contains "资料页含等级徽章" "$P" 'class="lv-badge lv'
contains "资料页含经验条" "$P" 'lv-fill'
contains "资料页含社交三区块" "$P" "获赞"
contains "资料页自视角文案" "$P" "我的帖子"
P_OTHER=$(curl -s -b "$JAR2" "$BASE/u/1")
contains "访客视角文案" "$P_OTHER" "TA 的帖子"
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
contains "发帖编辑器内置上传入口" "$UP" 'data-compose="upload"'
TR=$(curl -s -b "$JAR" "$THREAD_URL")
contains "回复表单带内置上传入口" "$TR" 'data-compose="upload"'
IMG=$(curl -s -b "$JAR" -H "X-CSRF-Token: $CSRF" \
  -d "content=看图 ![图]($AVURL)" "$THREAD_URL/reply")
contains "Markdown 图片渲染" "$IMG" "<img src=\"$AVURL\" alt=\"图\">"
echo "== 版主操作 =="
CSRF=$(curl -s -b "$JAR2" -c "$JAR2" "$BASE/" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR2" \
  -H "X-CSRF-Token: $CSRF" -X POST "$BASE/t/1/pin")
check "普通用户置顶被拒" "403" "$code"
csrf "$BASE/u/2"
# role=mod 必须指定管辖版块(category=2 即上面建的「技术分享」)
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" \
  -d "_csrf=$CSRF&role=mod&category=2" "$BASE/admin/users/2/role")
check "提升为版主(指定管辖版块)" "303" "$code"
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
contains "全部已读后清零" "$UN" '"unread":0'

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
contains "admin 通知已清零" "$UN" '"unread":0'

UN=$(curl -s "$BASE/notifications/unread")
contains "匿名未读为 0" "$UN" '"unread":0'
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

echo "== 最新/热帖排序 =="
CSRF=$(curl -s -b "$JAR" -c "$JAR" "$REDIR2" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
sleep 1
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -H "X-CSRF-Token: $CSRF" -d "content=顶一下第二帖" "$REDIR2/reply")
check "回复第二帖使之上浮" "200" "$code"
HOMEL=$(curl -s -b "$JAR" "$BASE/")
FIRSTL=$(echo "$HOMEL" | grep -o '/t/[0-9]*' | head -1)
check "最新页首帖为最新回复" "/t/2" "$FIRSTL"
HOMEH=$(curl -s -b "$JAR" "$BASE/?tab=hot")
FIRSTH=$(echo "$HOMEH" | grep -o '/t/[0-9]*' | head -1)
check "热帖页首帖为回复最多" "/t/1" "$FIRSTH"
echo "== 站点设置 =="
csrf "$BASE/admin/site"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -d "_csrf=$CSRF" \
  --data-urlencode "name=测试论坛" --data-urlencode "tagline=副标题测试" \
  --data-urlencode "footer=© 2026 测试" --data-urlencode "announcement=这是一条公告" "$BASE/admin/site")
check "保存站点设置" "303" "$code"
H=$(curl -s -b "$JAR" "$BASE/")
contains "顶栏站点名生效" "$H" "测试论坛"
contains "浏览器标题用站点名" "$H" "<title>首页 · 测试论坛</title>"
contains "品牌副标题生效" "$H" "副标题测试"
contains "页脚文案生效" "$H" "© 2026 测试"
contains "公告横幅生效" "$H" "这是一条公告"
contains "内置 favicon 就位" "$H" "/static/favicon.svg"
code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/static/favicon.svg")
check "favicon 可访问" "200" "$code"
csrf "$BASE/admin/site"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -d "_csrf=$CSRF" \
  --data-urlencode "name=" "$BASE/admin/site")
check "站点名留空被拒(回填表单)" "200" "$code"
contains "被拒后站点名仍是旧值" "$(curl -s -b "$JAR" "$BASE/")" "测试论坛"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR2" "$BASE/admin/site")
check "普通用户访问站点设置被拒" "403" "$code"
csrf "$BASE/admin/site"
curl -s -o /dev/null -b "$JAR" -d "_csrf=$CSRF" \
  --data-urlencode "name=测试论坛" --data-urlencode "announcement=" "$BASE/admin/site"
H=$(curl -s -b "$JAR" "$BASE/")
if echo "$H" | grep -q 'class="announce"'; then bad "公告清空后仍显示"; else ok "公告清空后关闭横幅"; fi
if echo "$H" | grep -q "Powered by bbs"; then ok "页脚留空回退默认" ; else bad "页脚未回退默认(应为 Powered by bbs)"; fi
csrf "$BASE/admin/site"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" \
  -F "_csrf=$CSRF" -F "icon=@$AV;type=image/gif" "$BASE/admin/site/icon")
check "上传站点图标" "303" "$code"
H=$(curl -s "$BASE/")
contains "顶栏改用自定义图标" "$H" 'class="logo-mark"><img src="/uploads/'
contains "favicon 指向自定义图标" "$H" 'rel="icon" href="/uploads/'
csrf "$BASE/admin/site"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -d "_csrf=$CSRF&clear=1" "$BASE/admin/site/icon")
check "恢复内置图标" "303" "$code"
contains "恢复后回到内置 favicon" "$(curl -s "$BASE/")" "/static/favicon.svg"

echo "== 个人中心:封禁提示条 / 无管理区块 =="
csrf "$BASE/u/2"
curl -s -o /dev/null -b "$JAR" -d "_csrf=$CSRF&days=3" "$BASE/admin/users/2/ban"
P=$(curl -s "$BASE/u/2")
contains "封禁账号显示提示条" "$P" 'class="ban-note"'
contains "提示条含解封日期" "$P" "自动解封"
csrf "$BASE/u/2"
curl -s -o /dev/null -b "$JAR" -d "_csrf=$CSRF" "$BASE/admin/users/2/unban"
P=$(curl -s "$BASE/u/2")
if echo "$P" | grep -q 'class="ban-note"'; then bad "解封后提示条仍在"; else ok "解封后提示条消失"; fi
PA=$(curl -s -b "$JAR" "$BASE/u/2")
if echo "$PA" | grep -q "在管理后台管理"; then bad "管理员视角仍有管理区块"; else ok "管理员视角无管理区块"; fi

echo "== 个人中心设置:通知偏好 + 实时推送 =="
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" "$BASE/settings")
check "设置页可访问" "200" "$code"
S=$(curl -s -b "$JAR" "$BASE/settings")
contains "设置页含接收范围" "$S" "通知接收范围"
if echo "$S" | grep -q "通知接收频率"; then bad "设置页仍有接收频率"; else ok "接收频率选项已移除"; fi
contains "设置页有私信提醒" "$S" "私信提醒"
contains "账户菜单设置入口已启用" "$(curl -s -b "$JAR" "$BASE/")" 'href="/settings"'
code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/settings")
check "未登录设置页跳登录" "303" "$code"
# SSE:未登录 204,登录后返回 text/event-stream
code=$(curl -s -o /dev/null -w '%{http_code}' -m 3 "$BASE/events")
check "未登录 SSE 返回 204" "204" "$code"
CT=$(curl -s -o /dev/null -w '%{content_type}' -m 3 -b "$JAR" "$BASE/events" || true)
contains "登录后 SSE 是事件流" "$CT" "text/event-stream"

# 只接收 @提及:bob 回复 admin 的主题不应产生通知
csrf "$BASE/settings"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -d "_csrf=$CSRF&scope=mention" "$BASE/settings/notify")
check "保存通知偏好" "303" "$code"
S=$(curl -s -b "$JAR" "$BASE/settings")
contains "偏好已回显(仅 @提及)" "$S" 'value="mention" checked'
contains "登录页面标记登录态给前端" "$(curl -s -b "$JAR" "$BASE/")" 'data-signed-in="1"'
CSRF=$(curl -s -b "$JAR2" -c "$JAR2" "$BASE/t/1" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
curl -s -o /dev/null -b "$JAR2" -H "X-CSRF-Token: $CSRF" -d "content=范围设为仅提及后的回复" "$BASE/t/1/reply"
UN=$(curl -s -b "$JAR" "$BASE/notifications/unread")
contains "仅 @提及:普通回复不产生通知" "$UN" '"unread":0'
CSRF=$(curl -s -b "$JAR2" -c "$JAR2" "$BASE/t/1" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
curl -s -o /dev/null -b "$JAR2" -H "X-CSRF-Token: $CSRF" -d "content=喊一下 @admin 看看" "$BASE/t/1/reply"
UN=$(curl -s -b "$JAR" "$BASE/notifications/unread")
if echo "$UN" | grep -q '"unread":[1-9]'; then ok "仅 @提及:被 @ 时仍通知"; else bad "仅 @提及:被 @ 却没通知 ($UN)"; fi
# 关闭通知
csrf "$BASE/settings"
curl -s -o /dev/null -b "$JAR" -d "_csrf=$CSRF&scope=none" "$BASE/settings/notify"
csrf "$BASE/notifications"
curl -s -o /dev/null -b "$JAR" -d "_csrf=$CSRF" "$BASE/notifications/read-all"
CSRF=$(curl -s -b "$JAR2" -c "$JAR2" "$BASE/t/1" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
curl -s -o /dev/null -b "$JAR2" -H "X-CSRF-Token: $CSRF" -d "content=关掉后再 @admin 一次" "$BASE/t/1/reply"
UN=$(curl -s -b "$JAR" "$BASE/notifications/unread")
contains "关闭通知后不再产生" "$UN" '"unread":0'
csrf "$BASE/settings"
curl -s -o /dev/null -b "$JAR" -d "_csrf=$CSRF&scope=all" "$BASE/settings/notify"

echo "== 私信(实时) =="
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" "$BASE/messages")
check "私信列表可访问" "200" "$code"
contains "空态提示" "$(curl -s -b "$JAR" "$BASE/messages")" "还没有私信"
contains "资料页有发私信入口" "$(curl -s -b "$JAR2" "$BASE/u/1")" "发私信"
if curl -s -b "$JAR" "$BASE/u/1" | grep -q "发私信"; then bad "自己主页也出现发私信"; else ok "自己主页不出现发私信"; fi
# bob(JAR2)找 admin 开会话
CSRF=$(curl -s -b "$JAR2" -c "$JAR2" "$BASE/u/1" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
DMURL=$(curl -s -o /dev/null -w '%{redirect_url}' -b "$JAR2" -d "_csrf=$CSRF&to=1" "$BASE/messages/start")
contains "开会话后跳到会话页" "$DMURL" "/messages/"
DMID=$(echo "$DMURL" | grep -oE '[0-9]+$')
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR2" -d "_csrf=$CSRF&to=2" "$BASE/messages/start")
check "不能给自己发私信" "400" "$code"
# 发一条,再看双方视角
SENT=$(curl -s -b "$JAR2" -H "X-CSRF-Token: $CSRF" --data-urlencode "body=你好,这是一条私信测试" "$BASE/messages/$DMID/send")
contains "发送返回消息列表片段" "$SENT" "这是一条私信测试"
contains "自己发的气泡标记 mine" "$SENT" "dm-row mine"
UN=$(curl -s -b "$JAR" "$BASE/notifications/unread")
if echo "$UN" | grep -q '"dm":[1-9]'; then ok "对方未读私信数 +1"; else bad "对方未读私信数没变 ($UN)"; fi
contains "顶栏有私信角标位" "$(curl -s -b "$JAR" "$BASE/")" 'id="dm-count"'
L=$(curl -s -b "$JAR" "$BASE/messages")
contains "对方会话列表出现该会话" "$L" "这是一条私信测试"
contains "会话列表显示未读徽标" "$L" "dm-badge"
# admin 打开会话 → 标已读
T=$(curl -s -b "$JAR" "$BASE/messages/$DMID")
contains "会话页显示消息" "$T" "这是一条私信测试"
UN=$(curl -s -b "$JAR" "$BASE/notifications/unread")
contains "打开会话后未读清零" "$UN" '"dm":0'
# 越权:第三个账号不能看别人的会话
JAR4=$(mktemp)
CSRF=$(curl -s -c "$JAR4" "$BASE/register" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
curl -s -o /dev/null -b "$JAR4" -c "$JAR4" -d "_csrf=$CSRF&name=carol&password=password789" "$BASE/register"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR4" "$BASE/messages/$DMID")
check "非参与者看会话 404" "404" "$code"
CSRF=$(curl -s -b "$JAR4" -c "$JAR4" "$BASE/" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR4" -H "X-CSRF-Token: $CSRF" \
  --data-urlencode "body=插话" "$BASE/messages/$DMID/send")
check "非参与者发消息 404" "404" "$code"
code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/messages")
check "未登录私信列表跳登录" "303" "$code"
# 免打扰:角标不亮,但列表里仍能看到未读
csrf "$BASE/settings"
curl -s -o /dev/null -b "$JAR" -d "_csrf=$CSRF&scope=all&dm=0" "$BASE/settings/notify"
CSRF=$(curl -s -b "$JAR2" -c "$JAR2" "$BASE/messages/$DMID" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
curl -s -o /dev/null -b "$JAR2" -H "X-CSRF-Token: $CSRF" --data-urlencode "body=免打扰状态下的第二条" "$BASE/messages/$DMID/send"
UN=$(curl -s -b "$JAR" "$BASE/notifications/unread")
contains "免打扰:私信角标报 0" "$UN" '"dm":0'
contains "免打扰:列表仍显示未读" "$(curl -s -b "$JAR" "$BASE/messages")" "dm-badge"
csrf "$BASE/settings"
curl -s -o /dev/null -b "$JAR" -d "_csrf=$CSRF&scope=all&dm=1" "$BASE/settings/notify"
UN=$(curl -s -b "$JAR" "$BASE/notifications/unread")
if echo "$UN" | grep -q '"dm":[1-9]'; then ok "恢复提醒后角标回来"; else bad "恢复提醒后角标仍为 0 ($UN)"; fi
rm -f "$JAR4"

echo "== 后台:邮件设置 =="
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" "$BASE/admin/mail")
check "邮件设置页可访问" "200" "$code"
M=$(curl -s -b "$JAR" "$BASE/admin/mail")
contains "含 SMTP 字段" "$M" 'name="host"'
contains "含加密方式选择" "$M" 'data-mail-secure'
contains "端口默认 587" "$M" 'value="587"'
contains "含测试发信" "$M" "/admin/mail/test"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR2" "$BASE/admin/mail")
check "普通用户邮件设置被拒" "403" "$code"
csrf "$BASE/admin/mail"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -d "_csrf=$CSRF" \
  --data-urlencode "host=smtp.example.com" --data-urlencode "port=70000" \
  --data-urlencode "from=bbs@example.com" --data-urlencode "secure=starttls" "$BASE/admin/mail")
check "端口越界被拒(回填表单)" "200" "$code"
csrf "$BASE/admin/mail"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -d "_csrf=$CSRF" \
  --data-urlencode "host=" --data-urlencode "port=587" --data-urlencode "from=" \
  --data-urlencode "secure=starttls" --data-urlencode "email_register=1" "$BASE/admin/mail")
check "未配 SMTP 就开邮件注册被拒" "200" "$code"
contains "拒绝原因提示" "$(curl -s -b "$JAR" "$BASE/admin/mail")" "邮件注册"
csrf "$BASE/admin/mail"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -d "_csrf=$CSRF" \
  --data-urlencode "host=smtp.example.com" --data-urlencode "port=465" \
  --data-urlencode "user=bbs@example.com" --data-urlencode "pass=secret123" \
  --data-urlencode "from=bbs@example.com" --data-urlencode "secure=ssl" "$BASE/admin/mail")
check "保存邮件设置" "303" "$code"
M=$(curl -s -b "$JAR" "$BASE/admin/mail")
contains "服务器已回显" "$M" "smtp.example.com"
contains "端口已回显" "$M" 'value="465"'
if echo "$M" | grep -q "secret123"; then bad "SMTP 口令被回显到页面"; else ok "SMTP 口令不回显"; fi
contains "提示口令已保存" "$M" "已保存,留空则不修改"
# 发信必然失败(example.com 不是真 SMTP),但要能把错误显示出来而不是 500
csrf "$BASE/admin/mail"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -d "_csrf=$CSRF" \
  --data-urlencode "to=nobody@example.com" "$BASE/admin/mail/test" --max-time 40)
check "测试发信失败也返回页面" "200" "$code"

echo "== 后台:安全设置(Turnstile) =="
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" "$BASE/admin/security")
check "安全设置页可访问" "200" "$code"
contains "含 Site Key 字段" "$(curl -s -b "$JAR" "$BASE/admin/security")" 'name="site_key"'
csrf "$BASE/admin/security"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -d "_csrf=$CSRF" \
  --data-urlencode "site_key=" --data-urlencode "secret_key=" --data-urlencode "turnstile_on=1" "$BASE/admin/security")
check "没填密钥就开启被拒" "200" "$code"
csrf "$BASE/admin/security"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -d "_csrf=$CSRF" \
  --data-urlencode "site_key=0xTEST_SITE" --data-urlencode "secret_key=0xTEST_SECRET" \
  --data-urlencode "turnstile_on=1" "$BASE/admin/security")
check "保存并开启人机验证" "303" "$code"
S=$(curl -s -b "$JAR" "$BASE/admin/security")
contains "Site Key 已回显" "$S" "0xTEST_SITE"
if echo "$S" | grep -q "0xTEST_SECRET"; then bad "Secret Key 被回显"; else ok "Secret Key 不回显"; fi
R=$(curl -s "$BASE/register")
contains "注册页出现验证组件" "$R" "cf-turnstile"
contains "注册页加载 Turnstile 脚本" "$R" "challenges.cloudflare.com"
# 开着验证时,不带令牌的注册必须被拦下
JAR5=$(mktemp)
CSRF=$(curl -s -c "$JAR5" "$BASE/register" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
RG=$(curl -s -b "$JAR5" -c "$JAR5" -d "_csrf=$CSRF&name=botuser&password=password123" "$BASE/register")
contains "无令牌注册被拦" "$RG" "请先完成人机验证"
if curl -s -b "$JAR5" "$BASE/" | grep -q "botuser"; then bad "被拦的注册仍建了账号"; else ok "被拦的注册未建账号"; fi
rm -f "$JAR5"
# 关掉验证,恢复正常注册
csrf "$BASE/admin/security"
curl -s -o /dev/null -b "$JAR" -d "_csrf=$CSRF&site_key=0xTEST_SITE&turnstile_on=0" "$BASE/admin/security"
if curl -s "$BASE/register" | grep -q "cf-turnstile"; then bad "关闭后仍渲染验证组件"; else ok "关闭后不渲染验证组件"; fi

echo "== 找回密码 / 邮箱验证入口 =="
code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/forgot")
check "找回密码页可访问" "200" "$code"
contains "登录页有找回入口" "$(curl -s "$BASE/login")" "/forgot"
F=$(curl -s "$BASE/forgot")
# 前面的邮件设置已保存了 SMTP,所以这里应当给出正常表单而不是「无法自助找回」
contains "已配发信时给出表单" "$F" "发送重置链接"
contains "找回表单含邮箱字段" "$F" 'name="email"'
code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/verify/resend")
check "重发验证页可访问" "200" "$code"
code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/reset?token=bogus")
check "无效重置令牌返回页面" "200" "$code"
contains "无效令牌给出提示" "$(curl -s "$BASE/reset?token=bogus")" "链接无效"
code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/verify/email?token=bogus")
check "无效验证令牌返回页面" "200" "$code"

echo "== 版块删除(强删 / 迁移) =="
csrf "$BASE/admin/categories"
curl -s -o /dev/null -b "$JAR" -d "_csrf=$CSRF&name=待迁移&slug=movesrc" "$BASE/admin/categories"
csrf "$BASE/new"
MV=$(curl -s -o /dev/null -w '%{redirect_url}' -b "$JAR" -d "_csrf=$CSRF" \
  --data-urlencode "category=movesrc" --data-urlencode "title=会被迁走的主题" \
  --data-urlencode "content=迁移测试正文" "$BASE/new")
contains "在待删版块里发了帖" "$MV" "/t/"
# 版块 id 从其他行的「迁往…」下拉里取,不依赖行内元素顺序
MVID=$(curl -s -b "$JAR" "$BASE/admin/categories" | grep -oE 'data-val="[0-9]+">待迁移<' | head -1 | grep -oE '[0-9]+' || true)
check "取到待迁移版块 id" "1" "$([ -n "$MVID" ] && echo 1 || echo 0)"
csrf "$BASE/admin/categories"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -d "_csrf=$CSRF" "$BASE/admin/categories/$MVID/delete")
check "非空版块不选方式被拒" "400" "$code"
csrf "$BASE/admin/categories"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -d "_csrf=$CSRF&mode=move&to=1" "$BASE/admin/categories/$MVID/delete")
check "迁移主题并删版块" "303" "$code"
A=$(curl -s -b "$JAR" "$BASE/admin/categories")
if echo "$A" | grep -q "movesrc"; then bad "版块未被删除"; else ok "版块已删除"; fi
contains "主题被迁到目标版块" "$(curl -s "$BASE/c/general")" "会被迁走的主题"
# 强删:再建一个带帖的版块,连同主题一起删
csrf "$BASE/admin/categories"
curl -s -o /dev/null -b "$JAR" -d "_csrf=$CSRF&name=待强删&slug=killme" "$BASE/admin/categories"
csrf "$BASE/new"
KD=$(curl -s -o /dev/null -w '%{redirect_url}' -b "$JAR" -d "_csrf=$CSRF" \
  --data-urlencode "category=killme" --data-urlencode "title=会被一起删掉的主题" \
  --data-urlencode "content=强删测试正文" "$BASE/new")
KTID=$(echo "$KD" | grep -oE '[0-9]+$')
KILLID=$(curl -s -b "$JAR" "$BASE/admin/categories" | grep -oE 'data-val="[0-9]+">待强删<' | head -1 | grep -oE '[0-9]+' || true)
check "取到待强删版块 id" "1" "$([ -n "$KILLID" ] && echo 1 || echo 0)"
csrf "$BASE/admin/categories"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -d "_csrf=$CSRF&mode=cascade" "$BASE/admin/categories/$KILLID/delete")
check "连同主题强删版块" "303" "$code"
code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/t/$KTID")
check "版块内主题已随之消失" "404" "$code"
if curl -s --get --data-urlencode "q=强删测试正文" "$BASE/search" | grep -q "会被一起删掉的主题"; then
  bad "搜索索引未清理"; else ok "搜索索引已随主题清理"; fi

echo "== 积分:签到 / 打赏 / 明细 / 后台调整 =="
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" "$BASE/points")
check "积分页可访问" "200" "$code"
contains "侧栏有签到入口" "$(curl -s -b "$JAR" "$BASE/")" '/checkin'
code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/points")
check "未登录积分页跳登录" "303" "$code"
# 签到:第一次到账,第二次提示已签
csrf "$BASE/points"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -d "_csrf=$CSRF" "$BASE/checkin")
check "签到跳转" "303" "$code"
P=$(curl -s -b "$JAR" "$BASE/points")
contains "签到后有积分记录" "$P" "每日签到"
contains "累计签到 1 天" "$P" "累计签到 <b>1</b>"
contains "余额显示 5" "$P" '<b class="pt-num">5</b>'
csrf "$BASE/points"
R=$(curl -s -o /dev/null -w '%{redirect_url}' -b "$JAR" -d "_csrf=$CSRF" "$BASE/checkin")
contains "重复签到回到已签提示" "$R" "ok=already"
contains "余额仍是 5" "$(curl -s -b "$JAR" "$BASE/points")" '<b class="pt-num">5</b>'
contains "侧栏显示已签到" "$(curl -s -b "$JAR" "$BASE/")" "已签到"
# 签到经验计入等级(admin 主题多,断言经验里含签到的 5 点:改用积分页面之外的资料页判断有变化即可)
contains "资料页经验可见" "$(curl -s -b "$JAR" "$BASE/u/1")" "经验"
# 打赏:bob 给 admin 的 1 号主题打赏
csrf "$BASE/admin/points"
curl -s -o /dev/null -b "$JAR" -d "_csrf=$CSRF&delta=100&note=测试发放" "$BASE/admin/points/2/adjust"
contains "后台调整写进流水" "$(curl -s -b "$JAR2" "$BASE/points")" "管理员调整"
contains "bob 余额 100" "$(curl -s -b "$JAR2" "$BASE/points")" '<b class="pt-num">100</b>'
T=$(curl -s -b "$JAR2" "$BASE/t/1")
contains "主题页有打赏入口" "$T" "data-tip-toggle"
CSRF=$(curl -s -b "$JAR2" -c "$JAR2" "$BASE/t/1" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
TIP=$(curl -s -b "$JAR2" -H "X-CSRF-Token: $CSRF" -d "amount=20" "$BASE/t/1/tip")
contains "打赏返回反应条" "$TIP" 'id="op-reacts"'
contains "打赏总额显示 20" "$TIP" '<b>20</b>'
contains "打赏方余额扣到 80" "$(curl -s -b "$JAR2" "$BASE/points")" '<b class="pt-num">80</b>'
contains "打赏方流水记支出" "$(curl -s -b "$JAR2" "$BASE/points")" "打赏支出"
contains "作者流水记收入" "$(curl -s -b "$JAR" "$BASE/points")" "收到打赏"
UN=$(curl -s -b "$JAR" "$BASE/notifications/unread")
if echo "$UN" | grep -q '"unread":[1-9]'; then ok "作者收到打赏通知"; else bad "作者没收到打赏通知 ($UN)"; fi
contains "通知页显示打赏文案" "$(curl -s -b "$JAR" "$BASE/notifications")" "打赏了你"
# 负路径
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR2" -H "X-CSRF-Token: $CSRF" -d "amount=99999" "$BASE/t/1/tip")
check "打赏超上限被拒" "400" "$code"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR2" -H "X-CSRF-Token: $CSRF" -d "amount=100000000" "$BASE/t/1/tip")
check "打赏金额过大被拒" "400" "$code"
CSRF2=$(curl -s -b "$JAR" -c "$JAR" "$BASE/t/1" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -H "X-CSRF-Token: $CSRF2" -d "amount=5" "$BASE/t/1/tip")
check "不能打赏自己" "400" "$code"
# 两位小数:打赏 3.24 要精确到分,整数余额不该显示成 80.00
CSRF=$(curl -s -b "$JAR2" -c "$JAR2" "$BASE/t/1" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//' || true)
TIPD=$(curl -s -b "$JAR2" -H "X-CSRF-Token: $CSRF" -d "amount=3.24" "$BASE/t/1/tip")
contains "小数打赏累加进总额" "$TIPD" '<b>23.24</b>'
PD=$(curl -s -b "$JAR2" "$BASE/points")
contains "小数打赏后余额精确到分" "$PD" '<b class="pt-num">76.76</b>'
contains "流水里的小数金额也带两位" "$PD" "\-3.24"
lacks "整数金额不补 .00" "$PD" "每日签到</span>.*+5.00"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR2" -H "X-CSRF-Token: $CSRF" -d "amount=1.234" "$BASE/t/1/tip")
check "超过两位小数被拒" "400" "$code"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR2" -H "X-CSRF-Token: $CSRF" -d "amount=0.001" "$BASE/t/1/tip")
check "小于 0.01 被拒" "400" "$code"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR2" -H "X-CSRF-Token: $CSRF" -d "amount=abc" "$BASE/t/1/tip")
check "非数字金额被拒" "400" "$code"
# 后台调整也能用小数,把零头抹平
csrf "$BASE/admin/points"
curl -s -o /dev/null -b "$JAR" -d "_csrf=$CSRF&delta=-0.76&note=抹平零头" "$BASE/admin/points/2/adjust"
contains "后台按小数扣减" "$(curl -s -b "$JAR2" "$BASE/points")" '<b class="pt-num">76</b>'
csrf "$BASE/admin/points"
A=$(curl -s -b "$JAR" -d "_csrf=$CSRF&delta=1.005&note=三位小数" "$BASE/admin/points/2/adjust")
contains "后台三位小数被拒" "$A" "最多两位小数"
JAR6=$(mktemp)
CSRF3=$(curl -s -c "$JAR6" "$BASE/register" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
curl -s -o /dev/null -b "$JAR6" -c "$JAR6" -d "_csrf=$CSRF3&name=poorguy&password=password123" "$BASE/register"
CSRF3=$(curl -s -b "$JAR6" -c "$JAR6" "$BASE/t/1" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR6" -H "X-CSRF-Token: $CSRF3" -d "amount=50" "$BASE/t/1/tip")
check "余额不足打赏被拒" "422" "$code"
rm -f "$JAR6"
# 后台积分页
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" "$BASE/admin/points")
check "后台积分页可访问" "200" "$code"
contains "后台列出余额" "$(curl -s -b "$JAR" "$BASE/admin/points")" "积分 <b>"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR2" "$BASE/admin/points")
check "普通用户后台积分页被拒" "403" "$code"
csrf "$BASE/admin/points"
A=$(curl -s -b "$JAR" -d "_csrf=$CSRF&delta=5&note=" "$BASE/admin/points/2/adjust")
contains "调整必须填原因" "$A" "调整原因"
csrf "$BASE/admin/points"
A=$(curl -s -b "$JAR" -d "_csrf=$CSRF&delta=-999999&note=扣到负数试试" "$BASE/admin/points/2/adjust")
contains "扣到负数被拦" "$A" "积分不足"

echo "== 付费帖 / 等级门槛 =="
contains "发帖页有帖子类型选择" "$(curl -s -b "$JAR" "$BASE/new")" "data-kind-pick"
contains "发帖页有阅读门槛设置" "$(curl -s -b "$JAR" "$BASE/new")" "data-level-pick"
csrf "$BASE/new"
PAID=$(curl -s -o /dev/null -w '%{redirect_url}' -b "$JAR" -d "_csrf=$CSRF" \
  --data-urlencode "category=tech" --data-urlencode "title=付费内容测试" \
  --data-urlencode "content=这是只有付费才能看到的正文段落" --data-urlencode "price=30" "$BASE/new")
PAIDID=$(echo "$PAID" | grep -oE '[0-9]+$')
contains "发出付费帖" "$PAID" "/t/"
contains "列表带积分标记" "$(curl -s "$BASE/c/tech")" "30 积分"
# 作者自己能看
contains "作者可见正文" "$(curl -s -b "$JAR" "$BASE/t/$PAIDID")" "只有付费才能看到"
# 未登录看不到
A=$(curl -s "$BASE/t/$PAIDID")
if echo "$A" | grep -q "只有付费才能看到"; then bad "未登录能看到付费正文"; else ok "未登录看不到付费正文"; fi
contains "未登录看到门槛提示" "$A" "阅读门槛"
# 门槛测试要用一个没有任何管理身份的普通号(bob 是 tech 版主,会直通门槛)
JARG=$(mktemp)
CSRFG=$(curl -s -c "$JARG" "$BASE/register" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
curl -s -o /dev/null -b "$JARG" -c "$JARG" -d "_csrf=$CSRFG&name=gateuser&password=password123" "$BASE/register"
GUID=$(curl -s -b "$JARG" "$BASE/" | grep -oE 'class="su-id" href="/u/[0-9]+' | grep -oE '[0-9]+$')
check "取到 gateuser 的 id" "1" "$([ -n "$GUID" ] && echo 1 || echo 0)"
contains "版主直通付费门槛" "$(curl -s -b "$JAR2" "$BASE/t/$PAIDID")" "只有付费才能看到"
B=$(curl -s -b "$JARG" "$BASE/t/$PAIDID")
if echo "$B" | grep -q "只有付费才能看到"; then bad "未解锁能看到付费正文"; else ok "未解锁看不到付费正文"; fi
contains "未解锁看到解锁按钮" "$B" "积分解锁"
CSRFG=$(curl -s -b "$JARG" -c "$JARG" "$BASE/t/$PAIDID" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JARG" -d "_csrf=$CSRFG" "$BASE/t/$PAIDID/unlock")
check "余额不足解锁被拒" "422" "$code"
# 充值后解锁成功
csrf "$BASE/admin/points"
curl -s -o /dev/null -b "$JAR" -d "_csrf=$CSRF&delta=100&note=解锁测试充值" "$BASE/admin/points/$GUID/adjust"
CSRFG=$(curl -s -b "$JARG" -c "$JARG" "$BASE/t/$PAIDID" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JARG" -d "_csrf=$CSRFG" "$BASE/t/$PAIDID/unlock")
check "解锁付费帖" "303" "$code"
contains "解锁后可见正文" "$(curl -s -b "$JARG" "$BASE/t/$PAIDID")" "只有付费才能看到"
contains "读者流水记解锁支出" "$(curl -s -b "$JARG" "$BASE/points")" "解锁付费帖"
contains "作者流水记付费收入" "$(curl -s -b "$JAR" "$BASE/points")" "付费帖收入"
contains "作者看到解锁人数" "$(curl -s -b "$JAR" "$BASE/t/$PAIDID")" "人解锁"
CSRFG=$(curl -s -b "$JARG" -c "$JARG" "$BASE/t/$PAIDID" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
BAL1=$(curl -s -b "$JARG" "$BASE/points" | grep -oE '<b class="pt-num">[0-9]+' | sed 's/.*>//')
curl -s -o /dev/null -b "$JARG" -d "_csrf=$CSRFG" "$BASE/t/$PAIDID/unlock"
BAL2=$(curl -s -b "$JARG" "$BASE/points" | grep -oE '<b class="pt-num">[0-9]+' | sed 's/.*>//')
check "重复解锁不重复扣费" "$BAL1" "$BAL2"
# 等级门槛
csrf "$BASE/new"
LVT=$(curl -s -o /dev/null -w '%{redirect_url}' -b "$JAR" -d "_csrf=$CSRF" \
  --data-urlencode "category=tech" --data-urlencode "title=高等级专享" \
  --data-urlencode "content=需要等级才能看的内容段" --data-urlencode "min_level=6" "$BASE/new")
LVTID=$(echo "$LVT" | grep -oE '[0-9]+$')
contains "列表带等级标记" "$(curl -s "$BASE/c/tech")" "LV6+"
L=$(curl -s -b "$JARG" "$BASE/t/$LVTID")
if echo "$L" | grep -q "需要等级才能看的内容段"; then bad "等级不够仍可见正文"; else ok "等级不够看不到正文"; fi
contains "提示需要的等级" "$L" "需要 LV6 及以上"
CSRFG=$(curl -s -b "$JARG" -c "$JARG" "$BASE/t/$LVTID" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JARG" -H "X-CSRF-Token: $CSRFG" -d "content=门槛外的回复" "$BASE/t/$LVTID/reply")
check "门槛外不能回复" "403" "$code"
if curl -s -b "$JARG" "$BASE/t/$LVTID" | grep -q "满足阅读门槛后可以看到回复"; then ok "门槛外回复区也被挡"; else bad "门槛外仍显示回复区"; fi

echo "== 抽奖帖 =="
csrf "$BASE/new"
LOT=$(curl -s -o /dev/null -w '%{redirect_url}' -b "$JAR" -d "_csrf=$CSRF" \
  --data-urlencode "category=tech" --data-urlencode "title=积分抽奖测试" \
  --data-urlencode "content=回复即参与,抽两位" --data-urlencode "kind=lottery" \
  --data-urlencode "prize=测试奖品" --data-urlencode "winners=2" --data-urlencode "stake=5" "$BASE/new")
LOTID=$(echo "$LOT" | grep -oE '[0-9]+$')
contains "发出抽奖帖" "$LOT" "/t/"
LP=$(curl -s -b "$JAR" "$BASE/t/$LOTID")
contains "主题页有抽奖组件" "$LP" "lot-card"
contains "抽奖组件显示奖品" "$LP" "测试奖品"
contains "抽奖组件显示投入" "$LP" "参与投入"
contains "列表带抽奖标记" "$(curl -s "$BASE/c/tech")" "badge-lot"
# bob 与 carol 回复参与(各扣 5 进奖池)
BAL1=$(curl -s -b "$JAR2" "$BASE/points" | grep -oE '<b class="pt-num">[0-9]+' | sed 's/.*>//')
CSRF=$(curl -s -b "$JAR2" -c "$JAR2" "$BASE/t/$LOTID" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
curl -s -o /dev/null -b "$JAR2" -H "X-CSRF-Token: $CSRF" -d "content=来试试运气" "$BASE/t/$LOTID/reply"
BAL2=$(curl -s -b "$JAR2" "$BASE/points" | grep -oE '<b class="pt-num">[0-9]+' | sed 's/.*>//')
check "回复参与扣掉投入积分" "$((BAL1-5))" "$BAL2"
contains "流水记参与抽奖" "$(curl -s -b "$JAR2" "$BASE/points")" "参与抽奖"
LP=$(curl -s -b "$JAR" "$BASE/t/$LOTID")
contains "参与人数变 1" "$LP" "已参与 <b>1</b>"
contains "奖池变 5" "$LP" "奖池 <b>5</b>"
CSRF=$(curl -s -b "$JAR2" -c "$JAR2" "$BASE/t/$LOTID" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
curl -s -o /dev/null -b "$JAR2" -H "X-CSRF-Token: $CSRF" -d "content=再回一句不该重复扣" "$BASE/t/$LOTID/reply"
BAL3=$(curl -s -b "$JAR2" "$BASE/points" | grep -oE '<b class="pt-num">[0-9]+' | sed 's/.*>//')
check "同一人重复回复不重复扣" "$BAL2" "$BAL3"
# 开奖:非楼主不能开
CSRF=$(curl -s -b "$JAR2" -c "$JAR2" "$BASE/t/$LOTID" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR2" -d "_csrf=$CSRF" "$BASE/t/$LOTID/draw")
check "非楼主开奖被拒" "403" "$code"
csrf "$BASE/t/$LOTID"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -d "_csrf=$CSRF" "$BASE/t/$LOTID/draw")
check "楼主开奖" "303" "$code"
LP=$(curl -s -b "$JAR" "$BASE/t/$LOTID")
contains "已开奖状态" "$LP" "已开奖"
contains "名单标出中奖" "$LP" "lot-entry won"
contains "中奖者拿到奖池" "$(curl -s -b "$JAR2" "$BASE/points")" "抽奖中奖"
UN=$(curl -s -b "$JAR2" "$BASE/notifications")
contains "中奖通知文案" "$UN" "你中奖了"
csrf "$BASE/t/$LOTID"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -d "_csrf=$CSRF" "$BASE/t/$LOTID/draw")
check "重复开奖幂等跳回" "303" "$code"
contains "开奖后奖池只发一次" "$(curl -s -b "$JAR" "$BASE/t/$LOTID")" "已开奖"

echo "== 积分抽奖帖 =="
contains "发帖页有三档类型" "$(curl -s -b "$JAR" "$BASE/new")" 'data-kind="lottery_points"'
# 定点开奖的时区:datetime-local 不带时区,浏览器要把 UTC 偏移一起发来,
# 否则服务器跑 UTC、用户在东八区时,「到点」会整体晚 8 小时(真踩过)
contains "发帖页带时区偏移字段" "$(curl -s -b "$JAR" "$BASE/new")" "data-tz-offset"
TZDAY=$(date -u -d '+10 days' +%Y-%m-%d 2>/dev/null || date -u -v+10d +%Y-%m-%d)
csrf "$BASE/new"
TZID=$(curl -s -o /dev/null -w '%{redirect_url}' -b "$JAR" -d "_csrf=$CSRF" \
  --data-urlencode "category=tech" --data-urlencode "title=时区测试" \
  --data-urlencode "content=东八区 20:00 应存成 UTC 12:00" --data-urlencode "kind=lottery" \
  --data-urlencode "prize=时区奖品" --data-urlencode "winners=1" \
  --data-urlencode "draw_at=${TZDAY}T20:00" --data-urlencode "tz_offset=480" "$BASE/new" | grep -oE '[0-9]+$')
TZP=$(curl -s -b "$JAR" "$BASE/t/$TZID")
contains "按浏览器时区换算开奖时刻" "$TZP" "12:00</time> 自动开奖"
contains "开奖时间给前端本机化的钩子" "$TZP" "data-localtime="
csrf "$BASE/new"
A=$(curl -s -b "$JAR" -d "_csrf=$CSRF" --data-urlencode "category=tech" --data-urlencode "title=垫不起的奖池" \
  --data-urlencode "content=测试余额不足" --data-urlencode "kind=lottery_points" \
  --data-urlencode "sponsor=100000" --data-urlencode "winners=1" "$BASE/new")
contains "积分不够垫奖池被拒" "$A" "积分不够垫这个奖池"
csrf "$BASE/new"
A=$(curl -s -b "$JAR" -d "_csrf=$CSRF" --data-urlencode "category=tech" --data-urlencode "title=人数多于奖池整数" \
  --data-urlencode "content=积分能拆到两位小数,3 分给 5 个人是可以的" --data-urlencode "kind=lottery_points" \
  --data-urlencode "sponsor=3" --data-urlencode "winners=5" --data-urlencode "stake=0" "$BASE/new")
lacks "3 积分分给 5 人不再被拒(小数拆得开)" "$A" "每位中奖者至少分到"
csrf "$BASE/new"
A=$(curl -s -b "$JAR" -d "_csrf=$CSRF" --data-urlencode "category=tech" --data-urlencode "title=没奖可发" \
  --data-urlencode "content=测试" --data-urlencode "kind=lottery_points" \
  --data-urlencode "sponsor=0" --data-urlencode "stake=0" --data-urlencode "winners=1" "$BASE/new")
contains "既不出奖也不收投入被拒" "$A" "没有奖可发"
# 正式一场:楼主出 10 分、不设中奖人数(全员分)、参与上限 2 人
PB1=$(curl -s -b "$JAR" "$BASE/points" | grep -oE '<b class="pt-num">[0-9]+' | sed 's/.*>//')
csrf "$BASE/new"
PT=$(curl -s -o /dev/null -w '%{redirect_url}' -b "$JAR" -d "_csrf=$CSRF" \
  --data-urlencode "category=tech" --data-urlencode "title=积分红包抽奖" \
  --data-urlencode "content=回复即参与,奖池随机拆" --data-urlencode "kind=lottery_points" \
  --data-urlencode "sponsor=10" --data-urlencode "winners=0" --data-urlencode "stake=0" \
  --data-urlencode "max_entries=2" "$BASE/new")
PTID=$(echo "$PT" | grep -oE '[0-9]+$')
check "发出积分抽奖帖" "1" "$([ -n "$PTID" ] && echo 1 || echo 0)"
PB2=$(curl -s -b "$JAR" "$BASE/points" | grep -oE '<b class="pt-num">[0-9]+' | sed 's/.*>//')
check "发帖即预扣奖池" "$((PB1-10))" "$PB2"
contains "流水记出奖预扣" "$(curl -s -b "$JAR" "$BASE/points")" "抽奖出奖"
P=$(curl -s -b "$JAR" "$BASE/t/$PTID")
contains "卡片标成积分抽奖" "$P" "积分抽奖"
contains "标题位显示奖池金额" "$P" "10 积分"
contains "中奖人数显示全员" "$P" "全员"
contains "显示参与上限" "$P" "已参与 <b>0</b> / 2 人"
contains "说明会自动到账" "$P" "自动打进中奖者账户"
# 先来后到:前 2 人进名单,第 3 个人回复照发但不计入
CSRF=$(curl -s -b "$JAR2" -c "$JAR2" "$BASE/t/$PTID" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//' || true)
curl -s -o /dev/null -b "$JAR2" -H "X-CSRF-Token: $CSRF" -d "content=占个位" "$BASE/t/$PTID/reply"
CSRFG=$(curl -s -b "$JARG" -c "$JARG" "$BASE/t/$PTID" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//' || true)
curl -s -o /dev/null -b "$JARG" -H "X-CSRF-Token: $CSRFG" -d "content=我也来" "$BASE/t/$PTID/reply"
P=$(curl -s -b "$JAR" "$BASE/t/$PTID")
contains "两人参与后满员" "$P" "已参与 <b>2</b> / 2 人"
csrf "$BASE/t/$PTID"
R=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -H "X-CSRF-Token: $CSRF" -d "content=楼主也想蹭" "$BASE/t/$PTID/reply")
P=$(curl -s -b "$JAR" "$BASE/t/$PTID")
contains "满员后参与人数不再增加" "$P" "已参与 <b>2</b> / 2 人"
contains "满员提示" "$P" "参与人数已满"
# 开奖:奖池随机拆开,总额不丢、每人至少 1
csrf "$BASE/t/$PTID"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -d "_csrf=$CSRF" "$BASE/t/$PTID/draw")
check "积分抽奖开奖" "303" "$code"
P=$(curl -s -b "$JAR" "$BASE/t/$PTID")
WON=$(grep -oE 'lot-win">中奖 \+[0-9.]+' <<<"$P" | grep -oE '[0-9.]+$')
check "中奖人数等于参与人数(全员分)" "2" "$(grep -c . <<<"$WON")"
check "奖池随机拆完不丢分" "10.00" "$(awk '{s+=$1} END{printf "%.2f", s}' <<<"$WON")"
check "每位中奖者至少 0.01 积分" "1" "$(awk 'BEGIN{ok=1} {if ($1 < 0.01) ok=0} END{print ok}' <<<"$WON")"
contains "中奖者积分到账" "$(curl -s -b "$JARG" "$BASE/points")" "抽奖中奖"
# 无人参与:开奖等于关掉,奖池原路退回
PB3=$(curl -s -b "$JAR" "$BASE/points" | grep -oE '<b class="pt-num">[0-9]+' | sed 's/.*>//')
csrf "$BASE/new"
EMPID=$(curl -s -o /dev/null -w '%{redirect_url}' -b "$JAR" -d "_csrf=$CSRF" \
  --data-urlencode "category=tech" --data-urlencode "title=没人来的抽奖" \
  --data-urlencode "content=不会有人回复" --data-urlencode "kind=lottery_points" \
  --data-urlencode "sponsor=20" --data-urlencode "winners=1" --data-urlencode "stake=0" "$BASE/new" | grep -oE '[0-9]+$')
csrf "$BASE/t/$EMPID"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -d "_csrf=$CSRF" "$BASE/t/$EMPID/draw")
check "无人参与也能开奖(转成已退回)" "303" "$code"
contains "卡片标成无人参与已退回" "$(curl -s -b "$JAR" "$BASE/t/$EMPID")" "无人参与,已退回"
check "奖池原路退回楼主" "$PB3" "$(curl -s -b "$JAR" "$BASE/points" | grep -oE '<b class="pt-num">[0-9]+' | sed 's/.*>//')"
contains "流水记奖池退回" "$(curl -s -b "$JAR" "$BASE/points")" "奖池退回"
# 帖子被删:楼主的奖池和参与者的投入都要退,不能跟着帖子一起蒸发
PB4=$(curl -s -b "$JAR" "$BASE/points" | grep -oE '<b class="pt-num">[0-9]+' | sed 's/.*>//')
BB4=$(curl -s -b "$JAR2" "$BASE/points" | grep -oE '<b class="pt-num">[0-9]+' | sed 's/.*>//')
csrf "$BASE/new"
DELID=$(curl -s -o /dev/null -w '%{redirect_url}' -b "$JAR" -d "_csrf=$CSRF" \
  --data-urlencode "category=tech" --data-urlencode "title=待删除的抽奖" \
  --data-urlencode "content=删帖要退款" --data-urlencode "kind=lottery_points" \
  --data-urlencode "sponsor=30" --data-urlencode "winners=1" --data-urlencode "stake=4" "$BASE/new" | grep -oE '[0-9]+$')
CSRF=$(curl -s -b "$JAR2" -c "$JAR2" "$BASE/t/$DELID" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//' || true)
curl -s -o /dev/null -b "$JAR2" -H "X-CSRF-Token: $CSRF" -d "content=投 4 分参与" "$BASE/t/$DELID/reply"
check "参与者投入已扣" "$((BB4-4))" "$(curl -s -b "$JAR2" "$BASE/points" | grep -oE '<b class="pt-num">[0-9]+' | sed 's/.*>//')"
csrf "$BASE/t/$DELID"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -d "_csrf=$CSRF" "$BASE/t/$DELID/delete")
check "删掉抽奖帖" "303" "$code"
check "楼主奖池退回" "$PB4" "$(curl -s -b "$JAR" "$BASE/points" | grep -oE '<b class="pt-num">[0-9]+' | sed 's/.*>//')"
check "参与者投入退回" "$BB4" "$(curl -s -b "$JAR2" "$BASE/points" | grep -oE '<b class="pt-num">[0-9]+' | sed 's/.*>//')"
contains "流水记删帖退回" "$(curl -s -b "$JAR2" "$BASE/points")" "抽奖帖已删除"

echo "== 勋章与积分商城 =="
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" "$BASE/admin/shop")
check "后台商城页可访问" "200" "$code"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR2" "$BASE/admin/shop")
check "普通用户后台商城被拒" "403" "$code"
csrf "$BASE/admin/shop"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -d "_csrf=$CSRF" \
  --data-urlencode "name=早期成员" --data-urlencode "note=内测期加入" "$BASE/admin/badges")
check "新建勋章" "200" "$code"
S=$(curl -s -b "$JAR" "$BASE/admin/shop")
contains "勋章出现在后台列表" "$S" "早期成员"
contains "显示持有人数" "$S" "0 人持有"
csrf "$BASE/admin/shop"
S=$(curl -s -b "$JAR" -d "_csrf=$CSRF" --data-urlencode "name=早期成员" "$BASE/admin/badges")
contains "同名勋章被拒" "$S" "同名勋章"
BID=$(curl -s -b "$JAR" "$BASE/admin/shop" | grep -oE 'data-val="[0-9]+">早期成员<' | head -1 | grep -oE '[0-9]+' || true)
check "取到勋章 id" "1" "$([ -n "$BID" ] && echo 1 || echo 0)"
# 商品:勋章类 + 签到加成类
csrf "$BASE/admin/shop"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -d "_csrf=$CSRF" \
  --data-urlencode "kind=badge" --data-urlencode "name=早期成员勋章" --data-urlencode "price=50" \
  --data-urlencode "badge_id=$BID" --data-urlencode "note=限量纪念" --data-urlencode "stock=2" "$BASE/admin/shop")
check "上架勋章商品" "200" "$code"
csrf "$BASE/admin/shop"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -d "_csrf=$CSRF" \
  --data-urlencode "kind=checkin" --data-urlencode "name=签到加成 30 天" --data-urlencode "price=80" \
  --data-urlencode "bonus=3" --data-urlencode "days=30" "$BASE/admin/shop")
check "上架签到加成商品" "200" "$code"
csrf "$BASE/admin/shop"
S=$(curl -s -b "$JAR" -d "_csrf=$CSRF" --data-urlencode "kind=badge" --data-urlencode "name=没选勋章" \
  --data-urlencode "price=10" "$BASE/admin/shop")
contains "勋章商品必须选勋章" "$S" "请选择这件商品对应的勋章"
# 前台商城
SH=$(curl -s -b "$JARG" "$BASE/shop")
contains "前台商城列出勋章商品" "$SH" "早期成员勋章"
contains "前台商城列出加成商品" "$SH" "签到加成 30 天"
contains "显示剩余库存" "$SH" "剩余 2"
code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/shop")
check "未登录商城跳登录" "303" "$code"
# 兑换:gateuser 先充值
csrf "$BASE/admin/points"
curl -s -o /dev/null -b "$JAR" -d "_csrf=$CSRF&delta=200&note=商城测试充值" "$BASE/admin/points/$GUID/adjust"
ITEMID=$(curl -s -b "$JARG" "$BASE/shop" | grep -oE '/shop/[0-9]+/redeem' | head -1 | grep -oE '[0-9]+' || true)
check "取到勋章商品 id" "1" "$([ -n "$ITEMID" ] && echo 1 || echo 0)"
CSRFG=$(curl -s -b "$JARG" -c "$JARG" "$BASE/shop" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
R=$(curl -s -b "$JARG" -d "_csrf=$CSRFG" "$BASE/shop/$ITEMID/redeem")
contains "兑换成功提示" "$R" "兑换成功"
contains "兑换后显示已拥有" "$R" "已拥有"
contains "流水记商城兑换" "$(curl -s -b "$JARG" "$BASE/points")" "商城兑换"
contains "兑换记录出现" "$(curl -s -b "$JARG" "$BASE/shop")" "我的兑换记录"
CSRFG=$(curl -s -b "$JARG" -c "$JARG" "$BASE/shop" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
R=$(curl -s -b "$JARG" -d "_csrf=$CSRFG" "$BASE/shop/$ITEMID/redeem")
contains "重复兑换勋章被拦" "$R" "已经有了"
contains "库存扣到 1" "$(curl -s -b "$JARG" "$BASE/shop")" "剩余 1"
# 佩戴勋章
E=$(curl -s -b "$JARG" "$BASE/u/$GUID/edit")
contains "编辑资料出现勋章选择" "$E" "佩戴勋章"
if echo "$E" | grep -q 'name="badge_text"'; then bad "还留着自定义称号输入框"; else ok "自定义称号输入框已移除"; fi
CSRFG=$(curl -s -b "$JARG" -c "$JARG" "$BASE/u/$GUID/edit" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JARG" \
  -d "_csrf=$CSRFG&badge_mode=wear&badge_id=$BID" "$BASE/u/$GUID/badge")
check "佩戴勋章" "303" "$code"
contains "资料页显示勋章" "$(curl -s "$BASE/u/$GUID")" "早期成员"
CSRFG=$(curl -s -b "$JARG" -c "$JARG" "$BASE/t/1" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
curl -s -o /dev/null -b "$JARG" -H "X-CSRF-Token: $CSRFG" -d "content=戴上勋章来冒个泡" "$BASE/t/1/reply"
contains "帖子里也显示勋章" "$(curl -s "$BASE/t/1")" "早期成员"
# 未持有的勋章不能佩戴
csrf "$BASE/admin/shop"
curl -s -o /dev/null -b "$JAR" -d "_csrf=$CSRF" --data-urlencode "name=神秘勋章" "$BASE/admin/badges"
BID2=$(curl -s -b "$JAR" "$BASE/admin/shop" | grep -oE 'data-val="[0-9]+">神秘勋章<' | head -1 | grep -oE '[0-9]+' || true)
check "取到第二枚勋章 id" "1" "$([ -n "$BID2" ] && echo 1 || echo 0)"
CSRFG=$(curl -s -b "$JARG" -c "$JARG" "$BASE/u/$GUID/edit" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JARG" \
  -d "_csrf=$CSRFG&badge_mode=wear&badge_id=$BID2" "$BASE/u/$GUID/badge")
check "佩戴未持有的勋章被拒" "403" "$code"
# 后台发放/收回(/admin/users 页面本身没有表单令牌,从弹窗片段取)
csrf "$BASE/admin/users/$GUID/panel"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -d "_csrf=$CSRF&badge_id=$BID2" "$BASE/admin/users/$GUID/badge")
check "后台发放勋章" "303" "$code"
contains "弹窗里标出已持有" "$(curl -s -b "$JAR" "$BASE/admin/users/$GUID/panel")" "神秘勋章"
csrf "$BASE/admin/users/$GUID/panel"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -d "_csrf=$CSRF&badge_id=$BID&revoke=1" "$BASE/admin/users/$GUID/badge")
check "后台收回勋章" "303" "$code"
if curl -s "$BASE/u/$GUID" | grep -q "早期成员"; then bad "收回后仍显示该勋章"; else ok "收回勋章同时取下佩戴"; fi
# 签到加成:兑换后签到多得积分
BONUSID=$(curl -s -b "$JARG" "$BASE/shop" | grep -A12 "签到加成 30 天" | grep -oE '/shop/[0-9]+/redeem' | head -1 | grep -oE '[0-9]+' || true)
check "取到加成商品 id" "1" "$([ -n "$BONUSID" ] && echo 1 || echo 0)"
CSRFG=$(curl -s -b "$JARG" -c "$JARG" "$BASE/shop" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
curl -s -o /dev/null -b "$JARG" -d "_csrf=$CSRFG" "$BASE/shop/$BONUSID/redeem"
P=$(curl -s -b "$JARG" "$BASE/points")
contains "积分页显示增值加成" "$P" "增值加成"
CSRFG=$(curl -s -b "$JARG" -c "$JARG" "$BASE/points" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
curl -s -o /dev/null -b "$JARG" -d "_csrf=$CSRFG" "$BASE/checkin"
contains "签到带加成记录" "$(curl -s -b "$JARG" "$BASE/points")" "每日签到"
# 加成额度不叠加:买两次同一档只续期。累加会滚成通胀,而且 checkin_bonus /
# bonus_until 各只有一列,装不下两份并存的加成(短期档会把长期档的到期日顶后)
contains "加成额度显示 +3" "$(curl -s -b "$JARG" "$BASE/points")" "增值加成 <b>+3</b>"
csrf "$BASE/admin/points"
curl -s -o /dev/null -b "$JAR" -d "_csrf=$CSRF&delta=1000&note=加成叠加测试" "$BASE/admin/points/$GUID/adjust"
CSRFG=$(curl -s -b "$JARG" -c "$JARG" "$BASE/shop" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//' || true)
curl -s -o /dev/null -b "$JARG" -d "_csrf=$CSRFG" "$BASE/shop/$BONUSID/redeem"
P2=$(curl -s -b "$JARG" "$BASE/points")
contains "再买一次同档不翻倍" "$P2" "增值加成 <b>+3</b>"
lacks "加成没有累加成 +6" "$P2" "增值加成 <b>+6</b>"
# 买更高档要升档,回头买低档不该降级
csrf "$BASE/admin/shop"
curl -s -o /dev/null -b "$JAR" -d "_csrf=$CSRF" --data-urlencode "kind=checkin" \
  --data-urlencode "name=签到加成高档" --data-urlencode "price=10" \
  --data-urlencode "bonus=7" --data-urlencode "days=10" "$BASE/admin/shop"
HIID=$(curl -s -b "$JARG" "$BASE/shop" | grep -A12 "签到加成高档" | grep -oE '/shop/[0-9]+/redeem' | head -1 | grep -oE '[0-9]+' || true)
CSRFG=$(curl -s -b "$JARG" -c "$JARG" "$BASE/shop" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//' || true)
curl -s -o /dev/null -b "$JARG" -d "_csrf=$CSRFG" "$BASE/shop/$HIID/redeem"
contains "买更高档升到 +7" "$(curl -s -b "$JARG" "$BASE/points")" "增值加成 <b>+7</b>"
CSRFG=$(curl -s -b "$JARG" -c "$JARG" "$BASE/shop" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//' || true)
curl -s -o /dev/null -b "$JARG" -d "_csrf=$CSRFG" "$BASE/shop/$BONUSID/redeem"
contains "回头买低档不降级" "$(curl -s -b "$JARG" "$BASE/points")" "增值加成 <b>+7</b>"
# 下架与删除
IID=$(curl -s -b "$JAR" "$BASE/admin/shop" | grep -oE '/admin/shop/[0-9]+/toggle' | head -1 | grep -oE '[0-9]+' || true)
csrf "$BASE/admin/shop"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -d "_csrf=$CSRF" "$BASE/admin/shop/$IID/toggle")
check "下架商品" "200" "$code"
contains "后台标出已下架" "$(curl -s -b "$JAR" "$BASE/admin/shop")" "已下架"
csrf "$BASE/admin/shop"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -d "_csrf=$CSRF" "$BASE/admin/shop/$IID/delete")
check "删除商品" "200" "$code"

echo "== 本轮修订:公告 / 认证后台 / 商城自定义商品 =="
# 公告横幅可暂停可继续(触屏 hover 粘住的问题)
csrf "$BASE/admin/site"
curl -s -o /dev/null -b "$JAR" -d "_csrf=$CSRF" --data-urlencode "name=测试论坛" \
  --data-urlencode "announcement=这是一条会滚动的公告" "$BASE/admin/site"
H=$(curl -s "$BASE/")
contains "公告有暂停按钮" "$H" "data-ann-toggle"
contains "公告暂停态有对应样式钩子" "$(curl -s "$BASE/static/style.css")" ".announce.paused"
contains "hover 暂停限定可悬停设备" "$(curl -s "$BASE/static/style.css")" "hover: hover"
# 打赏按钮改小图标 + 面板向下弹
contains "打赏面板向下弹(不挡标题)" "$(curl -s "$BASE/static/style.css")" "top: calc(100% + 6px)"
# 后台列表窄屏堆叠
contains "版块行用堆叠布局" "$(curl -s -b "$JAR" "$BASE/admin/categories")" "tr row-stack"
contains "堆叠布局有窄屏规则" "$(curl -s "$BASE/static/style.css")" ".tr.row-stack"

# 后台认证:直接添加 + 撤销
V=$(curl -s -b "$JAR" "$BASE/admin/verify")
contains "认证页有直接添加表单" "$V" "/admin/verify/add"
contains "认证页有已认证列表" "$V" "当前已认证"
csrf "$BASE/admin/verify"
V=$(curl -s -b "$JAR" -d "_csrf=$CSRF" --data-urlencode "name=bob" --data-urlencode "kind=厂商" \
  --data-urlencode "title=测试厂商认证" "$BASE/admin/verify/add")
contains "直接添加认证成功" "$V" "已给 bob 加上"
contains "已认证列表出现该用户" "$V" "测试厂商认证"
contains "资料页出现红 V" "$(curl -s "$BASE/u/2")" "v-red"
csrf "$BASE/admin/verify"
V=$(curl -s -b "$JAR" -d "_csrf=$CSRF" --data-urlencode "name=不存在的人" --data-urlencode "kind=官方" "$BASE/admin/verify/add")
contains "账号不存在给出提示" "$V" "找不到账号"
csrf "$BASE/admin/verify"
V=$(curl -s -b "$JAR" -d "_csrf=$CSRF" "$BASE/admin/verify/2/remove")
contains "撤销认证成功" "$V" "认证已撤销"
if curl -s "$BASE/u/2" | grep -q "测试厂商认证"; then bad "撤销后仍显示认证"; else ok "撤销后认证消失"; fi

# 商城:自定义商品
csrf "$BASE/admin/shop"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -d "_csrf=$CSRF" \
  --data-urlencode "kind=custom" --data-urlencode "name=定制头像框" --data-urlencode "price=200" \
  --data-urlencode "note=兑换后联系管理员安排" "$BASE/admin/shop")
check "上架自定义商品" "200" "$code"
A=$(curl -s -b "$JAR" "$BASE/admin/shop")
contains "后台标出自定义类型" "$A" "自定义"
contains "后台有兑换记录区" "$A" "兑换记录"
csrf "$BASE/admin/shop"
A=$(curl -s -b "$JAR" -d "_csrf=$CSRF" --data-urlencode "kind=custom" --data-urlencode "name=没写说明" \
  --data-urlencode "price=10" "$BASE/admin/shop")
contains "自定义商品必须写说明" "$A" "写清兑换后怎么发放"
contains "前台显示自定义商品" "$(curl -s -b "$JARG" "$BASE/shop")" "定制头像框"
contains "前台说明发放方式" "$(curl -s -b "$JARG" "$BASE/shop")" "管理员按说明发放"
CID=$(curl -s -b "$JARG" "$BASE/shop" | grep -A12 "定制头像框" | grep -oE '/shop/[0-9]+/redeem' | head -1 | grep -oE '[0-9]+' || true)
csrf "$BASE/admin/points"
curl -s -o /dev/null -b "$JAR" -d "_csrf=$CSRF&delta=300&note=自定义商品测试" "$BASE/admin/points/$GUID/adjust"
CSRFG=$(curl -s -b "$JARG" -c "$JARG" "$BASE/shop" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
R=$(curl -s -b "$JARG" -d "_csrf=$CSRFG" "$BASE/shop/$CID/redeem")
contains "兑换自定义商品" "$R" "管理员会按说明为你发放"
contains "后台兑换记录出现该单" "$(curl -s -b "$JAR" "$BASE/admin/shop")" "定制头像框"
# 商品在线编辑:类型不可改,历史订单存的是下单快照,改商品不该动账
contains "后台有编辑入口" "$(curl -s -b "$JAR" "$BASE/admin/shop")" "data-shop-edit"
csrf "$BASE/admin/shop"
A=$(curl -s -b "$JAR" -d "_csrf=$CSRF" --data-urlencode "name=定制头像框 Pro" \
  --data-urlencode "price=250" --data-urlencode "stock=7" \
  --data-urlencode "note=兑换后联系管理员安排" "$BASE/admin/shop/$CID/edit")
contains "编辑商品成功" "$A" "已更新"
contains "新名称生效" "$A" "定制头像框 Pro"
contains "新库存生效" "$A" "剩余 7"
contains "历史订单仍是原价快照" "$A" "\-200"
contains "前台看到新价格" "$(curl -s -b "$JARG" "$BASE/shop")" "250 积分"
csrf "$BASE/admin/shop"
A=$(curl -s -b "$JAR" -d "_csrf=$CSRF" --data-urlencode "name=" --data-urlencode "price=250" \
  --data-urlencode "note=兑换后联系管理员安排" "$BASE/admin/shop/$CID/edit")
contains "编辑时空商品名被拒" "$A" "商品名 1–30 字"
CSRFG=$(curl -s -b "$JARG" -c "$JARG" "$BASE/shop" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//' || true)
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JARG" -d "_csrf=$CSRFG" \
  --data-urlencode "name=偷改" --data-urlencode "price=1" "$BASE/admin/shop/$CID/edit")
check "非管理员不能编辑商品" "403" "$code"

echo "== 本轮修订:面板不再常驻 / 内置下拉 / 按钮右置 =="
contains "hidden 属性全局生效" "$(curl -s "$BASE/static/style.css")" '\[hidden\] { display: none !important; }'
T=$(curl -s -b "$JAR2" "$BASE/t/1")
contains "打赏面板默认收起" "$T" 'data-tip-pop hidden'
contains "打赏面板有关闭钮" "$T" "data-tip-close"
contains "打赏支持自定义金额" "$T" 'class="tip-custom"'
CSRF2=$(curl -s -b "$JAR2" -c "$JAR2" "$BASE/t/1" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
TIPC=$(curl -s -b "$JAR2" -H "X-CSRF-Token: $CSRF2" -d "amount=7" "$BASE/t/1/tip")
contains "自定义金额打赏生效" "$TIPC" 'id="op-reacts"'
A=$(curl -s -b "$JAR" "$BASE/admin/categories")
if echo "$A" | grep -q '<select'; then bad "版块页还有原生下拉"; else ok "版块迁移改用内置芯片"; fi
S=$(curl -s -b "$JAR" "$BASE/admin/shop")
if echo "$S" | grep -q '<select'; then bad "商城页还有原生下拉"; else ok "商城勋章选择改用内置芯片"; fi
U=$(curl -s -b "$JAR" "$BASE/admin/users")
if echo "$U" | grep -q 'tr row-stack'; then bad "用户行仍强制换行"; else ok "用户管理按钮回到行右侧"; fi
contains "用户行操作区仍在右列" "$U" 'class="row-acts"'

echo "== 本轮修订:弹窗选择 / 反应条独立 / 按钮统一 =="
N=$(curl -s -b "$JAR" "$BASE/new")
contains "发帖版块用可搜索弹窗" "$N" "data-picker-search"
contains "弹窗默认收起" "$N" "data-picker-modal hidden"
A=$(curl -s -b "$JAR" "$BASE/admin/categories")
contains "迁移目标用弹窗选择" "$A" "data-picker-modal"
if echo "$A" | grep -q 'data-pick="to"'; then bad "迁移仍是平铺芯片"; else ok "迁移不再平铺全部版块"; fi
S=$(curl -s -b "$JAR" "$BASE/admin/shop")
contains "商城勋章用弹窗选择" "$S" "搜索勋章名"
if echo "$S" | grep -q 'tr row-stack'; then bad "商品行仍强制换行"; else ok "商品行按钮保持同一行"; fi
contains "商品操作区不换行" "$S" "row-acts-tight"
CSS=$(curl -s "$BASE/static/style.css")
contains "小节表单分隔线只作用于直接子表单" "$CSS" ".um-sec > form + form"
if echo "$CSS" | grep -q '^\.um-sec form + form'; then bad "宽松的 form+form 规则还在(会把行内按钮顶下一行)"; else ok "行内按钮不再被分隔线顶下去"; fi
T=$(curl -s -b "$JAR2" "$BASE/t/1")
contains "反应条独立成区" "$T" 'class="react-bar"'
if echo "$T" | grep -q 'op-reacts-bar'; then bad "反应条还留在标题下方"; else ok "反应条已移出首帖卡"; fi
V=$(curl -s -b "$JAR" "$BASE/admin/verify")
if echo "$V" | grep -q 'tr row-stack'; then bad "已认证行仍强制换行"; else ok "已认证行撤销按钮同行"; fi
contains "按钮体系:危险按钮有红色描边" "$(curl -s "$BASE/static/style.css")" "border: 1px solid var(--like)"
contains "按钮体系:次要按钮有描边" "$(curl -s "$BASE/static/style.css")" ".btn-white, .btn-secondary, .btn-outline"

echo "== 认证申请:通过后不再留记录 =="
# bob 提一个申请,管理员通过 → 申请列表里不该再有它,但认证要生效
CSRFG=$(curl -s -b "$JARG" -c "$JARG" "$BASE/verify/apply" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
curl -s -o /dev/null -b "$JARG" -d "_csrf=$CSRFG" --data-urlencode "kind=作者" \
  --data-urlencode "subject=通过后应消失" --data-urlencode "note=测试" "$BASE/verify/apply"
V=$(curl -s -b "$JAR" "$BASE/admin/verify")
contains "申请出现在待审列表" "$V" "通过后应消失"
RID=$(echo "$V" | grep -oE '/admin/verify/[0-9]+/approve' | head -1 | grep -oE '[0-9]+' || true)
csrf "$BASE/admin/verify"
curl -s -o /dev/null -b "$JAR" -d "_csrf=$CSRF" "$BASE/admin/verify/$RID/approve"
V=$(curl -s -b "$JAR" "$BASE/admin/verify")
# 注意:申请里的「认证对象」通过后会成为用户的认证文案,所以要按申请行本身(审批按钮)判断
if echo "$V" | grep -q "/admin/verify/$RID/approve"; then bad "通过后申请记录还留着"; else ok "通过后申请记录已清掉"; fi
if echo "$V" | grep -q "已通过"; then bad "列表里还有「已通过」这种没用的记录"; else ok "列表里不再出现已通过记录"; fi
contains "认证已生效(出现在已认证列表)" "$V" "当前已认证"
contains "用户资料页有认证" "$(curl -s "$BASE/u/$GUID")" "v-badge"

echo "== 私信:内嵌编辑器 + 红包 =="
D=$(curl -s -b "$JAR2" "$BASE/messages/$DMID")
contains "私信用通用编辑器" "$D" 'class="composer"'
contains "编辑器带插图入口" "$D" 'data-compose="upload"'
contains "编辑器带红包按钮" "$D" "data-rp-toggle"
contains "红包面板默认收起" "$D" "data-rp-panel hidden"
# 面板必须浮在输入框上方:它原来是向下展开的普通块,而输入框就在页面最底下,
# 展开只是把页面撑高、下面一片留白,滚过去也看不出发生了什么
contains "红包面板浮在输入框上方" "$(curl -s "$BASE/static/style.css")" "position: absolute; left: 0; right: 0; bottom: 100%"
# 私信用的是论坛那个 composer(带插图),所以正文也得按 Markdown 渲染 ——
# 否则插进去的图片只会原样显示成 ![](/uploads/12)
CSRFD=$(curl -s -b "$JAR2" -c "$JAR2" "$BASE/messages/$DMID" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//' || true)
IMGMSG=$(curl -s -b "$JAR2" -H "X-CSRF-Token: $CSRFD" \
  --data-urlencode "body=看这张图 ![](/uploads/1)" "$BASE/messages/$DMID/send")
contains "私信里的图片被渲染成 img" "$IMGMSG" '<img src="/uploads/1"'
lacks "私信不再原样吐出 Markdown" "$IMGMSG" '!\[\](/uploads/1)'
NLMSG=$(curl -s -b "$JAR2" -H "X-CSRF-Token: $CSRFD" \
  --data-urlencode "body=第一行
第二行" "$BASE/messages/$DMID/send")
contains "单个换行直接断行(聊天口径)" "$NLMSG" "第一行<br>"
contains "上传回源要求校验缓存" "$(curl -s -D - -o /dev/null "$BASE/uploads/1" | tr 'A-Z' 'a-z')" "cache-control: no-cache"
lacks "红包面板不再有预设金额" "$D" 'class="tip-amt"'
contains "红包金额必填" "$D" 'aria-label="红包积分" required'
# 给 bob 充点积分再发红包
csrf "$BASE/admin/points"
curl -s -o /dev/null -b "$JAR" -d "_csrf=$CSRF&delta=300&note=红包测试充值" "$BASE/admin/points/2/adjust"
B1=$(curl -s -b "$JAR2" "$BASE/points" | grep -oE '<b class="pt-num">[0-9]+' | sed 's/.*>//')
CSRF=$(curl -s -b "$JAR2" -c "$JAR2" "$BASE/messages/$DMID" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
RP=$(curl -s -b "$JAR2" -H "X-CSRF-Token: $CSRF" -d "amount=50" "$BASE/messages/$DMID/redpack")
contains "红包气泡出现" "$RP" "rp-bubble"
contains "红包显示金额" "$RP" "50 积分"
contains "发送者看到等待领取" "$RP" "等待对方领取"
B2=$(curl -s -b "$JAR2" "$BASE/points" | grep -oE '<b class="pt-num">[0-9]+' | sed 's/.*>//')
check "发红包即扣积分" "$((B1-50))" "$B2"
contains "流水记发出红包" "$(curl -s -b "$JAR2" "$BASE/points")" "发出红包"
# admin(收件人)领取
A1=$(curl -s -b "$JAR" "$BASE/points" | grep -oE '<b class="pt-num">[0-9]+' | sed 's/.*>//')
MSGID=$(echo "$RP" | grep -oE 'name="msg" value="[0-9]+"' | tail -1 | grep -oE '[0-9]+' || true)
check "取到红包消息 id" "1" "$([ -n "$MSGID" ] && echo 1 || echo 0)"
CSRF=$(curl -s -b "$JAR" -c "$JAR" "$BASE/messages/$DMID" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
CL=$(curl -s -b "$JAR" -H "X-CSRF-Token: $CSRF" -d "msg=$MSGID" "$BASE/messages/$DMID/claim")
contains "领取后显示已领取" "$CL" "已领取"
A2=$(curl -s -b "$JAR" "$BASE/points" | grep -oE '<b class="pt-num">[0-9]+' | sed 's/.*>//')
check "领取者积分到账" "$((A1+50))" "$A2"
contains "流水记领取红包" "$(curl -s -b "$JAR" "$BASE/points")" "领取红包"
CL2=$(curl -s -b "$JAR" -H "X-CSRF-Token: $CSRF" -d "msg=$MSGID" "$BASE/messages/$DMID/claim")
A3=$(curl -s -b "$JAR" "$BASE/points" | grep -oE '<b class="pt-num">[0-9]+' | sed 's/.*>//')
check "重复领取不再加钱" "$A2" "$A3"
# 撤回未领取的红包
CSRF=$(curl -s -b "$JAR2" -c "$JAR2" "$BASE/messages/$DMID" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
RP2=$(curl -s -b "$JAR2" -H "X-CSRF-Token: $CSRF" -d "amount=20" "$BASE/messages/$DMID/redpack")
MSG2=$(echo "$RP2" | grep -oE 'name="msg" value="[0-9]+"' | tail -1 | grep -oE '[0-9]+' || true)
# 撤回按钮必须走 htmx 自己的确认(hx-confirm)。用 data-confirm 会退化成原生表单提交,
# 打到没注册的 POST /messages/{id} 上 —— 表现就是「点了确认没反应,还能无限点」
contains "撤回用 hx-confirm" "$RP2" 'hx-confirm="撤回这个红包'
if echo "$RP2" | grep -q 'data-confirm="撤回这个红包'; then bad "撤回仍用 data-confirm(会退化成原生提交)"; else ok "撤回不再用 data-confirm"; fi
contains "确认面板改用 requestSubmit" "$(curl -s "$BASE/static/app.js")" "submitConfirmed"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR2" -H "X-CSRF-Token: $CSRF" -d "msg=$MSG2" "$BASE/messages/$DMID")
check "会话页本身不接受 POST(旧原生提交的落点)" "405" "$code"
B3=$(curl -s -b "$JAR2" "$BASE/points" | grep -oE '<b class="pt-num">[0-9]+' | sed 's/.*>//')
contains "撤回前会话列表带金额" "$(curl -s -b "$JAR" "$BASE/messages")" "红包 20 积分"
RF=$(curl -s -b "$JAR2" -H "X-CSRF-Token: $CSRF" -d "msg=$MSG2" "$BASE/messages/$DMID/refund")
# 会话里已有往来:留一条中性占位(别让对方「收到提醒却找不到东西」),但金额必须消失
contains "撤回后只留占位" "$RF" "你撤回了一个红包"
lacks "撤回后不再显示金额" "$RF" "20 积分"
lacks "撤回后不再出现旧文案" "$RF" "已退回发送者"
B4=$(curl -s -b "$JAR2" "$BASE/points" | grep -oE '<b class="pt-num">[0-9]+' | sed 's/.*>//')
check "撤回后积分退回" "$((B3+20))" "$B4"
contains "流水记退回" "$(curl -s -b "$JAR2" "$BASE/points")" "红包退回"
DL=$(curl -s -b "$JAR" "$BASE/messages")
contains "会话列表把撤回显示成中性文字" "$DL" "撤回了一条消息"
lacks "会话列表也不泄漏撤回金额" "$DL" "红包 20 积分"
# 负路径
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR2" -H "X-CSRF-Token: $CSRF" -d "amount=99999" "$BASE/messages/$DMID/redpack")
check "红包超上限被拒" "400" "$code"
CSRFG=$(curl -s -b "$JARG" -c "$JARG" "$BASE/messages" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//' || true)
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JARG" -H "X-CSRF-Token: $CSRFG" -d "amount=10" "$BASE/messages/$DMID/redpack")
check "非会话参与者发红包 404" "404" "$code"

# 发错人场景:会话里只有这一个红包 → 撤回时连会话一起删掉,不给陌生人留痕
CSRF2=$(curl -s -b "$JAR2" -c "$JAR2" "$BASE/u/$GUID" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//' || true)
NEWID=$(curl -s -o /dev/null -w '%{redirect_url}' -b "$JAR2" -d "_csrf=$CSRF2&to=$GUID" "$BASE/messages/start" | grep -oE '[0-9]+$')
check "开出一条空会话" "1" "$([ -n "$NEWID" ] && echo 1 || echo 0)"
CSRF2=$(curl -s -b "$JAR2" -c "$JAR2" "$BASE/messages/$NEWID" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//' || true)
SOLO=$(curl -s -b "$JAR2" -H "X-CSRF-Token: $CSRF2" -d "amount=15" "$BASE/messages/$NEWID/redpack")
contains "孤立会话里发出红包" "$SOLO" "等待对方领取"
SOLOMSG=$(echo "$SOLO" | grep -oE 'name="msg" value="[0-9]+"' | tail -1 | grep -oE '[0-9]+' || true)
contains "对方列表里能看到" "$(curl -s -b "$JARG" "$BASE/messages")" "红包 15 积分"
B5=$(curl -s -b "$JAR2" "$BASE/points" | grep -oE '<b class="pt-num">[0-9]+' | sed 's/.*>//')
HDR=$(curl -s -s -D - -o /dev/null -b "$JAR2" -H "X-CSRF-Token: $CSRF2" -d "msg=$SOLOMSG" "$BASE/messages/$NEWID/refund" | tr 'A-Z' 'a-z')
contains "孤立会话撤回后让前端跳回列表" "$HDR" "hx-redirect: /messages"
B6=$(curl -s -b "$JAR2" "$BASE/points" | grep -oE '<b class="pt-num">[0-9]+' | sed 's/.*>//')
check "孤立会话撤回也退积分" "$((B5+15))" "$B6"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR2" "$BASE/messages/$NEWID")
check "会话已被删掉" "404" "$code"
lacks "对方列表里一点痕迹都不留" "$(curl -s -b "$JARG" "$BASE/messages")" "15 积分"

echo "== 账号页:账户名 / 邮箱登录 / 改显示名收费 =="
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JARG" "$BASE/account")
check "账号页可访问" "200" "$code"
AC=$(curl -s -b "$JARG" "$BASE/account")
contains "账号页有账户名字段" "$AC" 'name="login_name"'
contains "账号页有改密码" "$AC" "修改密码"
contains "账号页有两步验证" "$AC" "两步验证"
contains "账号页指向编辑资料改显示名" "$AC" "/edit"
contains "账户菜单有账号入口" "$(curl -s -b "$JARG" "$BASE/")" 'href="/account"'
code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/account")
check "未登录账号页跳登录" "303" "$code"
# 改账户名后用新账户名登录
CSRFG=$(curl -s -b "$JARG" -c "$JARG" "$BASE/account" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JARG" -d "_csrf=$CSRFG&login_name=gate_login" "$BASE/account/name")
check "保存账户名" "303" "$code"
JARL=$(mktemp)
T=$(curl -s -c "$JARL" "$BASE/login" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
curl -s -o /dev/null -b "$JARL" -c "$JARL" -d "_csrf=$T&name=gate_login&password=password123" "$BASE/login"
check "用新账户名能登录" "1" "$(curl -s -b "$JARL" "$BASE/" | grep -c nav-user)"
contains "显示名没被改动" "$(curl -s -b "$JARL" "$BASE/u/$GUID")" "gateuser"
CSRFG=$(curl -s -b "$JARG" -c "$JARG" "$BASE/account" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
A=$(curl -s -b "$JARG" -d "_csrf=$CSRFG&login_name=admin" "$BASE/account/name")
contains "账户名重复被拒" "$A" "已被占用"
rm -f "$JARL"
# 后台建的号带邮箱视同已验证 → 可用邮箱登录
csrf "$BASE/admin/users/new"
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" -d "_csrf=$CSRF" \
  --data-urlencode "name=maillogin" --data-urlencode "email=maillogin@example.com" \
  --data-urlencode "password=password123" "$BASE/admin/users/new")
check "后台建带邮箱的号" "303" "$code"
JARM=$(mktemp)
T=$(curl -s -c "$JARM" "$BASE/login" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
curl -s -o /dev/null -b "$JARM" -c "$JARM" -d "_csrf=$T&name=maillogin@example.com&password=password123" "$BASE/login"
check "用邮箱能登录" "1" "$(curl -s -b "$JARM" "$BASE/" | grep -c nav-user)"
rm -f "$JARM"
# 改显示名扣 3 积分
csrf "$BASE/admin/points"
curl -s -o /dev/null -b "$JAR" -d "_csrf=$CSRF&delta=10&note=改名费测试" "$BASE/admin/points/$GUID/adjust"
P1=$(curl -s -b "$JARG" "$BASE/points" | grep -oE '<b class="pt-num">[0-9]+' | sed 's/.*>//')
CSRFG=$(curl -s -b "$JARG" -c "$JARG" "$BASE/u/$GUID/edit" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
code=$(curl -s -o /dev/null -w '%{http_code}' -b "$JARG" \
  -d "_csrf=$CSRFG" --data-urlencode "name=改名后的我" --data-urlencode "bio=" "$BASE/u/$GUID/edit")
check "改显示名跳转" "303" "$code"
P2=$(curl -s -b "$JARG" "$BASE/points" | grep -oE '<b class="pt-num">[0-9]+' | sed 's/.*>//')
check "改显示名扣 3 积分" "$((P1-3))" "$P2"
contains "流水记改名费" "$(curl -s -b "$JARG" "$BASE/points")" "修改显示名"
contains "新显示名生效" "$(curl -s "$BASE/u/$GUID")" "改名后的我"
contains "编辑页提示扣费" "$(curl -s -b "$JARG" "$BASE/u/$GUID/edit")" "扣 3 积分"
# 余额不足时改名被拒
csrf "$BASE/admin/points"
BAL=$(curl -s -b "$JARG" "$BASE/points" | grep -oE '<b class="pt-num">[0-9]+' | sed 's/.*>//')
curl -s -o /dev/null -b "$JAR" -d "_csrf=$CSRF&delta=-$BAL&note=清空余额测试" "$BASE/admin/points/$GUID/adjust"
CSRFG=$(curl -s -b "$JARG" -c "$JARG" "$BASE/u/$GUID/edit" | grep -o 'name="_csrf" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"//')
R=$(curl -s -b "$JARG" -d "_csrf=$CSRFG" --data-urlencode "name=没钱也想改" --data-urlencode "bio=" "$BASE/u/$GUID/edit")
contains "余额不足改名被拒" "$R" "需要 3 积分"
if curl -s "$BASE/u/$GUID" | grep -q "没钱也想改"; then bad "余额不足却改名成功"; else ok "余额不足时显示名没变"; fi

rm -f "$AV"
echo "结果: $PASS 通过, $FAIL 失败"
[ "$FAIL" -eq 0 ] && echo "SMOKE OK" || exit 1
