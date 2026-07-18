# syntax=docker/dockerfile:1.7

FROM golang:1.24 AS builder

ENV GOPROXY=https://goproxy.cn,direct \
    GO111MODULE=on \
    CGO_ENABLED=0

WORKDIR /app

COPY ./proto/go.* ./proto/
COPY ./proto/call/go.* ./proto/call/
COPY ./proto/data_forwarding/go.* ./proto/data_forwarding/
COPY ./proto/friend/go.* ./proto/friend/
COPY ./proto/push/go.* ./proto/push/
COPY ./proto/storage/go.* ./proto/storage/
COPY ./shared/go.* ./shared/
COPY ./services/pushService/go.* ./services/pushService/

WORKDIR /app/services/pushService

RUN --mount=type=cache,id=betterfly-go-mod,target=/go/pkg/mod \
    go mod download

WORKDIR /app

COPY ./proto ./proto
COPY ./shared ./shared
COPY ./services/pushService ./services/pushService

WORKDIR /app/services/pushService

RUN --mount=type=cache,id=betterfly-go-mod,target=/go/pkg/mod \
    --mount=type=cache,id=betterfly-go-build,target=/root/.cache/go-build \
    go build -trimpath -o pushMain .

FROM ubuntu:24.04

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && update-ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /app/services/pushService/pushMain .

ENTRYPOINT ["./pushMain"]
