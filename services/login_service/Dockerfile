# 第一阶段：构建
FROM golang:1.24 as builder

# 设置工作目录（项目根目录）
WORKDIR /app

# 复制整个项目（包含 shared）
COPY ./services/login_service ./services/login_service
COPY ./shared ./shared

# 进入 login_service 目录构建
WORKDIR /app/services/login_service

# 构建 login_main.go
RUN go mod tidy && go build -o login_main login_main.go

# 第二阶段：运行环境
FROM ubuntu:latest

WORKDIR /app
COPY --from=builder /app/services/login_service/login_main .
COPY --from=builder /app/shared/db_op/db_config/server.crt .

# 启动服务
ENTRYPOINT ["./login_main"]
