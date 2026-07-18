# syntax=docker/dockerfile:1.7

# 第一阶段：构建
FROM golang:1.24 AS builder

# 设置go mod下载镜像源
ENV GOPROXY=https://goproxy.cn,direct \
    GO111MODULE=on \
    CGO_ENABLED=0

# 设置工作目录（项目根目录）
WORKDIR /app

# 依赖清单与源码分层，避免业务代码变化后重新下载全部模块。
COPY ./proto/go.* ./proto/
COPY ./proto/call/go.* ./proto/call/
COPY ./proto/data_forwarding/go.* ./proto/data_forwarding/
COPY ./proto/friend/go.* ./proto/friend/
COPY ./proto/push/go.* ./proto/push/
COPY ./proto/storage/go.* ./proto/storage/
COPY ./shared/go.* ./shared/
COPY ./services/authService/go.* ./services/authService/

WORKDIR /app/services/authService

RUN --mount=type=cache,id=betterfly-go-mod,target=/go/pkg/mod \
    go mod download

WORKDIR /app

COPY ./proto ./proto
COPY ./shared ./shared
COPY ./services/authService ./services/authService

# 进入服务目录构建
WORKDIR /app/services/authService

# 构建缓存由所有 Go 服务共享。
RUN --mount=type=cache,id=betterfly-go-mod,target=/go/pkg/mod \
    --mount=type=cache,id=betterfly-go-build,target=/root/.cache/go-build \
    go build -trimpath -o authMain .

# 第二阶段：运行环境
FROM ubuntu:latest

WORKDIR /app

# 复制构建结果
COPY --from=builder /app/services/authService/authMain .

# 启动服务
ENTRYPOINT ["./authMain"]
