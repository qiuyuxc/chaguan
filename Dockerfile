# 多阶段构建:最终镜像只有一个静态二进制
FROM golang:1.27-alpine AS build
WORKDIR /src
ENV GOPROXY=https://goproxy.cn,direct
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bbs ./cmd/bbs

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/bbs /bbs
VOLUME /data
ENV BBS_DB=/data/bbs.db BBS_UPLOADS=/data/uploads PORT=8080
EXPOSE 8080
ENTRYPOINT ["/bbs"]
