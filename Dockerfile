# syntax=docker/dockerfile:1
# 纯 Go 静态二进制,底座只需要能跑一个文件;tzdata 编在二进制里。
# root 运行:docker 自动建的 bind mount 目录归 root,非 root uid 写不进去。
# gcr.io 不可达时:--build-arg BASE=scratch(根证书、/data、/tmp 由下面显式带入)
ARG BASE=gcr.io/distroless/static-debian12

FROM golang:1.27-alpine AS build
WORKDIR /src
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY} CGO_ENABLED=0
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/chaguan ./cmd/chaguan
RUN mkdir -p /out/data /out/tmp

FROM ${BASE}
COPY --from=build /out/chaguan /chaguan
# 发信/Turnstile 要验证书;multipart 溢出写 /tmp
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/data /data
COPY --from=build /out/tmp /tmp
VOLUME /data
ENV CHAGUAN_DB=/data/chaguan.db CHAGUAN_UPLOADS=/data/uploads PORT=8080 TZ=Asia/Shanghai
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD ["/chaguan", "-healthcheck"]
ENTRYPOINT ["/chaguan"]
