# 第一阶段：构建
FROM golang:1.24 as builder

# 设置工作目录（项目根目录）
WORKDIR /app

# 复制所有必要的模块文件和项目文件（关键修改）
COPY ./proto ./proto
COPY ./shared ./shared
COPY ./services/login_signup_service ./services/login_signup_service

# 进入服务目录构建
WORKDIR /app/services/login_signup_service

# 下载依赖并构建
RUN go mod tidy && \
    go build -o login_signup_main login_signup_main.go

# 第二阶段：运行环境
FROM ubuntu:latest

WORKDIR /app

# 复制构建结果和必要的文件
COPY --from=builder /app/services/login_signup_service/login_signup_main .
COPY --from=builder /app/shared/db_op/db_config/server.crt .

# 启动服务
ENTRYPOINT ["./login_signup_main"]