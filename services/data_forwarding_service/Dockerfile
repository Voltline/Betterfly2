# 1. 使用 Go 官方镜像作为构建阶段
FROM golang:1.20 AS builder

# 2. 设置工作目录
WORKDIR /service

# 3. 复制当前微服务的代码及依赖
COPY go.mod go.sum ./
RUN go mod tidy
RUN go mod download

# 4. 复制当前微服务代码
COPY . .

# 5. 编译 Go 可执行文件
RUN go build -o data_forwarding_main ./data_forwarding_main.go

# 6. 使用更小的基础镜像作为运行环境
FROM ubuntu:latest

WORKDIR /root/

# 7. 复制编译后的可执行文件
COPY --from=builder /service/data_forwarding_main .

# 调试：列出文件，检查 app 是否存在
RUN ls -lah .
RUN pwd

# 8. 运行微服务
CMD ["bash"]
