# syntax=docker/dockerfile:1
# chaguan:单二进制论坛。纯 Go(CGO_ENABLED=0),所以构建阶段之后只带一个可执行文件。
# 时区数据由 time/tzdata 编进二进制,底座里没有 zoneinfo 也能用 TZ=Asia/Shanghai。
#
# 底座默认 distroless static(无 shell、非 root)。gcr.io 不可达时可以换:
#   docker build --build-arg BASE=scratch .
# 换 scratch 时底座是空的,所以根证书、/data、/tmp 都由下面显式带进去。
ARG BASE=gcr.io/distroless/static-debian12:nonroot

FROM golang:1.27-alpine AS build
WORKDIR /src
# 国内网络可用 --build-arg GOPROXY=https://goproxy.cn,direct
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY} CGO_ENABLED=0
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
# 交叉编译由 buildx 通过 TARGETOS/TARGETARCH 注入
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/chaguan ./cmd/chaguan
# /data 必须在镜像里就存在:VOLUME 的匿名卷会照镜像里的属主初始化,
# 镜像里没有这个目录时 Docker 按 root:root 建,非 root 进程就没法在里面建 uploads。
# /tmp 是给 multipart 上传用的 —— 超过内存缓冲的部分会落到 os.TempDir()。
RUN mkdir -p /out/data /out/tmp

FROM ${BASE}
COPY --from=build /out/chaguan /chaguan
# 发信(SMTP over TLS)和 Turnstile 校验都要验对方证书,scratch 底座没有根证书
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
# 数据(SQLite + 上传文件)全部落在 /data,挂出去即可持久化。
# 属主必须是运行用户,否则匿名卷或宿主目录归 root 时进程写不进去。
COPY --from=build --chown=65532:65532 /out/data /data
COPY --from=build --chown=65532:65532 /out/tmp /tmp
# 65532 = distroless 的 nonroot;scratch 底座没有 /etc/passwd,所以用数字
USER 65532:65532
VOLUME /data
ENV CHAGUAN_DB=/data/chaguan.db CHAGUAN_UPLOADS=/data/uploads PORT=8080 TZ=Asia/Shanghai
EXPOSE 8080
# 镜像里没有 curl/wget,让二进制自己当探针
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD ["/chaguan", "-healthcheck"]
ENTRYPOINT ["/chaguan"]
