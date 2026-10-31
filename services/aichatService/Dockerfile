# syntax=docker/dockerfile:1.7

FROM golang:1.24 AS builder

ENV GOPROXY=https://goproxy.cn,direct \
    GO111MODULE=on \
    CGO_ENABLED=0

WORKDIR /app

COPY ./proto/go.* ./proto/
COPY ./proto/chatbot/go.* ./proto/chatbot/
COPY ./proto/storage/go.* ./proto/storage/
COPY ./shared/go.* ./shared/
COPY ./services/aichatService/go.* ./services/aichatService/

WORKDIR /app/services/aichatService

RUN --mount=type=cache,id=betterfly-go-mod,target=/go/pkg/mod \
    go mod download

WORKDIR /app

COPY ./proto ./proto
COPY ./shared ./shared
COPY ./services/aichatService ./services/aichatService

WORKDIR /app/services/aichatService

RUN --mount=type=cache,id=betterfly-go-mod,target=/go/pkg/mod \
    --mount=type=cache,id=betterfly-go-build,target=/root/.cache/go-build \
    go build -trimpath -o aichatMain .

FROM ubuntu:24.04

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && update-ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /app/services/aichatService/aichatMain .

ENTRYPOINT ["./aichatMain"]
