# 1. 使用 Go 官方镜像作为构建阶段
FROM golang:1.24 AS builder

# 2. 设置工作目录
WORKDIR /app

# 4. 复制当前微服务代码
COPY ./proto ./proto
COPY ./shared ./shared
COPY ./services/dataForwardingService ./services/dataForwardingService

# 5. 编译 Go 可执行文件
WORKDIR /app/services/dataForwardingService
RUN go mod tidy &&  \
    go build -o dataForwardingMain .

# 6. 使用更小的基础镜像作为运行环境
FROM ubuntu:latest

WORKDIR /app

# 7. 复制编译后的可执行文件
COPY --from=builder /app/services/dataForwardingService/dataForwardingMain .

# 8. 运行微服务
CMD ["./dataForwardingMain"]
