# 第一阶段：构建
FROM golang:1.24 AS builder

# 设置go mod下载镜像源
ENV GOPROXY=https://goproxy.cn,direct \
    GO111MODULE=on \
    CGO_ENABLED=0

# 设置工作目录（项目根目录）
WORKDIR /app

# 复制所有必要的模块文件和项目文件（关键修改）
COPY ./proto ./proto
COPY ./shared ./shared
COPY ./services/authService ./services/authService

# 进入服务目录构建
WORKDIR /app/services/authService

# 下载依赖并构建
RUN go mod tidy && \
    go build -o authMain .

# 第二阶段：运行环境
FROM ubuntu:latest

WORKDIR /app

# 复制构建结果和必要的文件
COPY --from=builder /app/services/authService/authMain .
COPY --from=builder /app/shared/db/config/server.crt .

# 启动服务
ENTRYPOINT ["./authMain"]