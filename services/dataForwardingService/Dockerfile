# syntax=docker/dockerfile:1.7

# 1. 使用 Go 官方镜像作为构建阶段
FROM golang:1.24 AS builder

# 设置go mod下载镜像源
ENV GOPROXY=https://goproxy.cn,direct \
    GO111MODULE=on \
    CGO_ENABLED=0

# 2. 设置工作目录
WORKDIR /app

# 3. 先下载依赖，业务源码变化时继续复用模块缓存。
COPY ./proto/go.* ./proto/
COPY ./proto/call/go.* ./proto/call/
COPY ./proto/data_forwarding/go.* ./proto/data_forwarding/
COPY ./proto/friend/go.* ./proto/friend/
COPY ./proto/push/go.* ./proto/push/
COPY ./proto/storage/go.* ./proto/storage/
COPY ./shared/go.* ./shared/
COPY ./services/dataForwardingService/go.* ./services/dataForwardingService/

WORKDIR /app/services/dataForwardingService

RUN --mount=type=cache,id=betterfly-go-mod,target=/go/pkg/mod \
    go mod download

WORKDIR /app

COPY ./proto ./proto
COPY ./shared ./shared
COPY ./services/dataForwardingService ./services/dataForwardingService

# 4. 编译 Go 可执行文件
WORKDIR /app/services/dataForwardingService
RUN --mount=type=cache,id=betterfly-go-mod,target=/go/pkg/mod \
    --mount=type=cache,id=betterfly-go-build,target=/root/.cache/go-build \
    go build -trimpath -o dataForwardingMain .

# 5. 使用更小的基础镜像作为运行环境
FROM ubuntu:latest

WORKDIR /app

# 6. 复制编译后的可执行文件
COPY --from=builder /app/services/dataForwardingService/dataForwardingMain .

# ✅ 复制证书
COPY --from=builder /app/services/dataForwardingService/certs ./certs

# 7. 设置证书路径
ENV CERT_PATH=./certs/cert.pem
ENV KEY_PATH=./certs/key.pem

# 8. 运行微服务
CMD ["./dataForwardingMain"]
