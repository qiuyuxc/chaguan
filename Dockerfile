# syntax=docker/dockerfile:1
# bbs:单二进制论坛。纯 Go(CGO_ENABLED=0),所以构建阶段之后什么都不用带。
# 时区数据由 time/tzdata 编进二进制,distroless 里也能用 TZ=Asia/Shanghai。

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
    go build -trimpath -ldflags="-s -w" -o /out/bbs ./cmd/bbs

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/bbs /bbs
# 数据(SQLite + 上传文件)全部落在 /data,挂出去即可持久化
VOLUME /data
ENV BBS_DB=/data/bbs.db BBS_UPLOADS=/data/uploads PORT=8080 TZ=Asia/Shanghai
EXPOSE 8080
# 镜像里没有 curl/wget,让二进制自己当探针
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD ["/bbs", "-healthcheck"]
ENTRYPOINT ["/bbs"]
