# bbs

Go + SQLite 单二进制论坛。服务端渲染(`html/template`)+ htmx 局部刷新,
模板与静态资源全部 `go:embed` 进二进制 —— 部署就是一个文件加一个数据目录,没有 Node 工具链。

## 快速开始

```bash
go run ./cmd/bbs          # http://localhost:8080,数据库在 ./data/bbs.db
```

首个注册的用户自动成为管理员。

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `PORT` | `8080` | 监听端口 |
| `BBS_DB` | `data/bbs.db` | SQLite 文件路径 |
| `BBS_UPLOADS` | `uploads` | 上传文件目录 |
| `TZ` | 系统时区 | 影响签到的「一天」与后台今日统计。启动日志会打印实际生效的时区 —— Android/Termux 上 Go 认不出系统 zoneinfo,所以程序显式按 `TZ` 装载(内嵌 tzdata),别只靠系统默认 |
| `BBS_RP_TTL` | `24h` | 私信红包没人领多久自动退回发送者 |
| `BBS_SWEEP` | `1m` | 后台巡检间隔(红包超时退回 + 抽奖定点开奖)。**定点开奖的精度就是这个值** —— 设定时刻最多晚一个周期才生效 |

SMTP、人机验证、站点品牌这些都在管理后台页面里配置,不需要环境变量。
后两个旋钮生产用默认值就行,存在主要是让测试脚本把「等 24 小时」压成「等 2 秒」。

## 功能

**社区**:注册/登录/登出、版块、主题与回复、Markdown 与图片上传、编辑(带「已编辑」标记)、
分页、全文搜索(FTS5 trigram,短查询自动回退 LIKE)、点赞/收藏、@提及、引用回复、
首页帖子流(最新/热帖 + 版块筛选)、个人主页与 LV0–LV6 等级、账号认证(官方/厂商/作者三色 V 标)。

**实时**:一条 SSE 长连接(`/events`)推送通知与私信信号,前端收到信号再拉数据;
长连接不可用时自动回退轮询。

**私信**:一对一实时会话,与论坛同一套编辑器(插图/@/表情/图标),支持发积分红包
(发出即扣款、对方领取到账、未领取可撤回退款)。

**积分**:每日签到(+5 积分 +5 经验)、打赏、付费帖(观看等级门槛 / 支付积分解锁)、
积分商城(勋章 / 签到加成 / 自定义商品)、积分流水可对账。支持两位小数 —— 打赏和私信
红包可以填 3.24,签到、改名费这类系统固定值仍是整数。

> 实现上积分**一律以「分」为单位存整数**(1 积分 = 100 分,见 `internal/db/points_unit.go`)。
> 绝不用浮点:float 算钱会累积误差,随机拆奖池再求和就对不上账。新增积分字段时记住存的是
> 「分」;展示走模板助手 `pts`,读用户输入走 `db.ParsePoints`,写常量用 `db.Pts(n)`。

**抽奖帖**:回复即参与,可设参与投入积分、参与人数上限(先来后到)、定点自动开奖。
分两类 —— **实物奖**平台只随机抽人、出中奖名单,奖品楼主自己发;**积分奖**由楼主发帖时
预扣一笔奖池(可与参与者投入合流),开奖时按随机切点法拆开自动打给中奖者,每人至少 0.01 积分。
开不了奖(无人参与)或帖子被删,奖池与投入都原路退回。

**勋章**:由后台发放、活动授予或积分兑换获得,用户自己选择佩戴哪一枚(或跟随身份 / 不显示)。

**账号**(`/account`):账户名(登录用)与显示名分离 —— 登录填账户名或已验证邮箱,
显示名在「编辑资料」里改(每次扣 3 积分)。支持绑定/更换邮箱、修改密码、
两步验证(TOTP,标准库自实现,开启后登录多一步验证码)。

**管理后台**(`/admin`,独立布局):概览、用户管理(弹窗内改资料/头像/密码/勋章/等级/社交数据/认证/封禁/版主授权)、
内容管理、版块管理(空版块直接删,非空版块可「连同主题删除」或「先迁移主题」)、
认证管理(直接增删 + 申请审批)、积分管理、商城与勋章、站点设置(品牌/页脚/图标/公告)、
邮件设置(SMTP + 邮件注册开关 + 测试发信)、安全设置(Cloudflare Turnstile)。

**账号安全**:可选的邮箱注册验证与忘记密码找回(一次性令牌,重置后踢掉全部会话);
CSRF 双提交校验;人机验证作用于注册/找回/重发验证邮件。

## 结构

```
cmd/bbs/            入口(含 -healthcheck 探针)
internal/db/        SQLite 打开/迁移/全部 SQL(migrations/ 内嵌)
internal/auth/      bcrypt、会话 token、TOTP、请求上下文认证信息
internal/mail/      SMTP 发信(587 STARTTLS / 465 SSL / 明文)
internal/markdown/  正文渲染(goldmark;原始 HTML 与危险链接被过滤)
internal/handlers/  路由、中间件、全部 handler(含 SSE 推送 hub)
web/                模板 + 静态资源(全部 go:embed,含 htmx.min.js)
preview/            早期移动端 UI 静态稿,仅作设计参考,不参与构建
scripts/            端到端测试脚本
```

数据库不用 ORM,SQL 全在 `internal/db`;迁移只做加列/加表,旧二进制挂新库也能启动。

## 测试

```bash
go build -o bbs ./cmd/bbs
PORT=8090 BBS_DB=/tmp/t.db BBS_UPLOADS=/tmp ./bbs &   # 起一个空库实例
BASE=http://localhost:8090 bash scripts/smoke.sh      # 515 条 curl 断言
bash scripts/mailflow.sh                              # 邮件链路 16 条(python3 起假 SMTP)
bash scripts/accountflow.sh                           # 两步验证 18 条(python3 算 TOTP)
bash scripts/sweeper.sh                               # 巡检 24 条(红包超时退回 + 定点开奖)
```

`smoke.sh` 与 `sweeper.sh` 只依赖 curl,另两套额外需要 python3(不用装任何第三方库)。
`sweeper.sh` 自己起实例并把 `BBS_RP_TTL` 压到 2 秒;定点开奖那段压不了(`datetime-local`
只精确到分钟),会等到下一个整分,所以这套要跑一分钟左右。CI 会跑全部四套。

## 部署

### Docker Compose(本地构建)

```bash
docker compose up -d --build
# 国内网络:GOPROXY=https://goproxy.cn,direct docker compose up -d --build
```

### 用 CI 构建好的镜像

```bash
docker run -d --name bbs -p 8080:8080 \
  -e TZ=Asia/Shanghai -v "$PWD/data:/data" \
  ghcr.io/qiuyuxc/bbs:latest
```

镜像基于 distroless(无 shell、非 root),提供 `amd64` 与 `arm64`。
数据(SQLite + 上传文件)全在 `/data`,挂出去即可持久化,换镜像不影响数据。
时区数据已编进二进制,`TZ` 直接生效。容器探活用二进制自带的 `bbs -healthcheck`。

### 不用 Docker

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o bbs ./cmd/bbs
```

或直接下 Release 里的 `bbs_<版本>_linux_<架构>.tar.gz`。

## CI(GitHub Actions)

| 工作流 | 触发 | 做什么 |
|---|---|---|
| `ci.yml` | push / PR | `go vet`、编译、跑三套端到端测试 |
| `build.yml` | 默认分支 / `v*` 标签 | buildx 多架构构建并推 `ghcr.io/<owner>/bbs`(`latest`、语义化版本、短哈希) |
| `release.yml` | `v*` 标签 | 交叉编译 amd64/arm64 二进制,连 `checksums.txt` 挂到 Release |

## 许可

[MIT](LICENSE)
