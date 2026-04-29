FROM golang:1.24 AS builder

ENV GOPROXY=https://goproxy.cn,direct \
    GO111MODULE=on \
    CGO_ENABLED=0

WORKDIR /app

COPY ./shared ./shared
COPY ./services/chatbotService ./services/chatbotService

WORKDIR /app/services/chatbotService

RUN go mod tidy && \
    go build -o chatbotMain .

FROM ubuntu:latest

WORKDIR /app

COPY --from=builder /app/services/chatbotService/chatbotMain .

ENTRYPOINT ["./chatbotMain"]
