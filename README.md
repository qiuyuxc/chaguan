<div align="center"><img src="web/static/favicon.svg" width="72" alt="chaguan" /></div>

# chaguan

Go + SQLite 单二进制论坛。服务端渲染(`html/template`)+ htmx 局部刷新,模板与静态资源全部 `go:embed` 进二进制 —— 部署就是一个文件加一个数据目录,没有 Node 工具链。

## 功能

| 领域 | 功能 |
| --- | --- |
| 主题与回复 | Markdown、图片上传、编辑留痕、分页、置顶锁定 |
| 互动 | 点赞、收藏、打赏、@提及、引用回复 |
| 搜索 | 全文搜索(FTS5 trigram),短查询回退 LIKE |
| 首页 | 最新 / 热帖帖子流,版块筛选 |
| 私信 | 一对一实时会话,积分红包(可撤回、超时退回) |
| 实时 | SSE 推送通知与私信信号,断线回退轮询 |
| 等级与认证 | LV0–LV6 等级、三色认证 V、勋章自选佩戴 |
| 积分 | 签到、付费帖(等级门槛 / 积分解锁)、商城、流水对账,两位小数 |
| 抽奖帖 | 回复即参与;实物奖抽人、积分奖自动拆池;定点开奖、退款 |
| 账号 | 账户名与显示名分离、邮箱绑定、TOTP 两步验证 |
| 安全 | bcrypt、CSRF 双提交、邮箱验证与找回、Turnstile 人机验证 |
| 后台 | 用户、内容、版块、认证、积分、商城、站点、邮件、安全 |

## 架构

```text
┌────────────┐  HTML / 局部片段   ┌──────────────┐   单连接串行   ┌────────────┐
│   浏览器    │ ◀──────────────▶ │ chaguan 二进制 │ ────────────▶ │ SQLite WAL │
│ htmx 局部刷新 │      SSE 信号     │ net/http + SSR │               │  + 上传文件  │
└────────────┘                   └──────────────┘               └────────────┘
```

## 项目结构

```text
chaguan
├── cmd
│   └── chaguan                  # 入口:环境变量、时区、迁移、优雅退出、-healthcheck 探针
│
├── internal
│   ├── handlers                 # 路由表、CSRF/会话中间件、全部页面 handler
│   │   ├── server.go            # 路由与中间件装配、后台巡检
│   │   ├── forum.go             # 版块、主题、回复、搜索
│   │   ├── gate.go              # 阅读门槛与抽奖
│   │   ├── dm.go                # 私信与红包
│   │   └── ...                  # auth/account/profile/shop/admin 等按域分文件
│   ├── db                       # 数据层,不用 ORM
│   │   ├── db.go                # 连接与全部 SQL(WAL + 单写连接)
│   │   ├── points_unit.go       # 积分单位换算(1 积分 = 100 分)
│   │   └── migrations/          # 迁移,只加列/加表
│   ├── auth                     # bcrypt、会话 token、TOTP
│   ├── mail                     # SMTP 发信(587 STARTTLS / 465 SSL / 明文)
│   └── markdown                 # goldmark 渲染,过滤危险协议链接
│
├── web                          # 模板与静态资源,全部 go:embed
│   ├── render.go                # 模板集与模板助手(pts / relTime / 徽章)
│   ├── templates                # 一页一个文件,partials/ 是复用片段
│   └── static                   # style.css、app.js、htmx.min.js、图标
│
├── scripts                      # 端到端测试:smoke / mailflow / accountflow / sweeper
│
├── Dockerfile                   # 多阶段构建,distroless 底座
└── docker-compose.yml           # 容器编排
```

## 快速开始

```bash
go run ./cmd/chaguan          # http://localhost:8080,数据库在 ./data/chaguan.db
```

首个注册的用户自动成为管理员。

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `PORT` | `8080` | 监听端口 |
| `CHAGUAN_DB` | `data/chaguan.db` | SQLite 文件路径 |
| `CHAGUAN_UPLOADS` | `uploads` | 上传文件目录 |
| `TZ` | 系统时区 | 影响签到的「一天」、后台今日统计、定点开奖 |
| `CHAGUAN_RP_TTL` | `24h` | 私信红包未领取自动退回的时限 |
| `CHAGUAN_SWEEP` | `1m` | 后台巡检间隔,定点开奖的精度就是它 |

SMTP、人机验证、站点品牌在管理后台页面里配置,不走环境变量。
后两个变量存在是让测试脚本把「等 24 小时」压成「等 2 秒」,生产用默认值。

## 部署

### Docker Compose(推荐)

`docker-compose.yml` 默认拉 CI 构建好的镜像:

```bash
docker compose up -d                        # latest
CHAGUAN_TAG=v0.1.0 docker compose up -d     # 钉某个版本
```

仓库私有时先 `docker login ghcr.io`(GitHub 用户名 + 有 `read:packages` 的 token)。
想从源码本地构建,把 compose 里的 `image` 注掉、换成注释里的 `build`:

```bash
docker compose up -d --build
# 国内网络:GOPROXY=https://goproxy.cn,direct docker compose up -d --build
```

### 只用 docker run

```bash
docker run -d --name chaguan -p 8080:8080 \
  -e TZ=Asia/Shanghai -v "$PWD/data:/data" \
  ghcr.io/qiuyuxc/chaguan:latest
```

镜像基于 distroless(无 shell),以 root 运行,提供 `amd64` 与 `arm64`。
数据(SQLite + 上传文件)全在 `/data`,挂出去即可持久化,换镜像不影响数据。
时区数据已编进二进制,`TZ` 直接生效。容器探活用 `chaguan -healthcheck`。

### 不用 Docker

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o chaguan ./cmd/chaguan
```

或直接下 Release 里的 `chaguan_<版本>_linux_<架构>.tar.gz`。

## 技术栈

| 层 | 技术 |
| --- | --- |
| 后端 | Go 标准库 net/http,无框架 |
| 前端 | html/template SSR + htmx,零构建、零 npm 依赖 |
| 存储 | SQLite(modernc.org/sqlite 纯 Go 驱动,WAL + 单写连接) |
| 交付 | 单二进制、多阶段 Docker 构建、GitHub Actions |

## 开发

```bash
go build -o chaguan ./cmd/chaguan
PORT=8090 CHAGUAN_DB=/tmp/t.db CHAGUAN_UPLOADS=/tmp ./chaguan &   # 起一个空库实例
BASE=http://localhost:8090 bash scripts/smoke.sh      # 516 条 curl 断言
bash scripts/mailflow.sh                              # 邮件链路 16 条(python3 起假 SMTP)
bash scripts/accountflow.sh                           # 两步验证 18 条(python3 算 TOTP)
bash scripts/sweeper.sh                               # 巡检 24 条(红包退回 + 定点开奖)
```

`sweeper.sh` 自己起实例并把 `CHAGUAN_RP_TTL` 压到 2 秒;定点开奖那段只精确到分钟,
所以这套要跑一分钟左右。CI 会跑全部四套。

两条贯穿全局的约定:

- **积分一律以「分」为单位存整数**(1 积分 = 100 分,`internal/db/points_unit.go`),不用浮点。
  展示走模板助手 `pts`,读用户输入走 `db.ParsePoints`,写常量用 `db.Pts`。
- **迁移只做加列 / 加表**,不改不删,旧二进制挂新库也能启动。

## CI

| 工作流 | 触发 | 做什么 |
| --- | --- | --- |
| `ci.yml` | push / PR | `go vet`、编译、四套端到端测试 |
| `build.yml` | 默认分支 / `v*` 标签 | buildx 多架构构建并推 `ghcr.io/qiuyuxc/chaguan`(`latest`、语义化版本、短哈希) |
| `release.yml` | `v*` 标签 | 交叉编译二进制,连 `checksums.txt` 挂到 Release |

## 许可

[MIT](LICENSE)
