# bbs

Go + SQLite 单二进制论坛。SSR(html/template)+ htmx 局部刷新,无 Node 工具链。
移动端 UI 已定版于 `preview/`,产品样式与其保持同步(`web/static/style.css`)。

## 开发

```bash
go run ./cmd/bbs          # http://localhost:8080,数据库在 ./data/bbs.db
```

首个注册用户自动成为管理员,可在首页创建版块。

环境变量:`PORT`(默认 8080)、`BBS_DB`(默认 data/bbs.db)、`BBS_UPLOADS`(默认 uploads)。

## 部署(Docker)

```bash
docker compose up -d --build
```

数据(SQLite + 上传文件)落在 `./data/` 卷,镜像回滚不影响数据。

从 Termux 直接交叉编译部署产物(不用 Docker 也能跑):

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bbs-linux ./cmd/bbs
```

## 结构

```
cmd/bbs/            入口
internal/db/        SQLite 打开/迁移/全部 SQL(migrations/ 内嵌)
internal/auth/      bcrypt、会话 token、请求上下文认证信息
internal/handlers/  路由、中间件、全部 handler
internal/markdown/  正文渲染(goldmark,CommonMark;原始 HTML/危险链接被过滤)
web/                模板 + 静态资源(全部 go:embed,含 htmx.min.js)
DESIGN.md           设计系统契约,AI 生成 UI 时照此执行
scripts/smoke.sh    curl 冒烟测试
```

## 阶段

- **已完成**:注册/登录/登出、版块/主题/回复/删除、分页、CSRF;
  Markdown 渲染、主题/回复编辑(带“已编辑”标记)、图片上传与回访、
  用户资料页/简介/头像、版主操作(置顶/锁定)、用户管理(版主任命/封禁/解封)、
  站内通知(回复 + @提及,铃铛 30s 轮询角标)、首页帖子流(最新/热帖 + 分类筛选)、
  版块管理后台、全文搜索(FTS5 trigram 索引,短查询自动回退 LIKE)、
  通知已读管理(单条点击已读 + 全部已读)、抽屉导航/桌面端侧栏(按 preview 定版)
- **剩余路线**:通知设置
