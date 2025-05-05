# 1. 使用 Go 官方镜像作为构建阶段
FROM golang:1.24 AS builder

# 2. 设置工作目录
WORKDIR /app

# 3. 复制当前微服务代码
COPY ./proto ./proto
COPY ./shared ./shared
COPY ./services/dataForwardingService ./services/dataForwardingService

# 4. 编译 Go 可执行文件
WORKDIR /app/services/dataForwardingService
RUN go mod tidy &&  \
    go build -o dataForwardingMain .

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