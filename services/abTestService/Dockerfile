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
COPY ./services/abTestService/go.* ./services/abTestService/

WORKDIR /app/services/abTestService

RUN --mount=type=cache,id=betterfly-go-mod,target=/go/pkg/mod \
    go mod download

WORKDIR /app

COPY ./proto ./proto
COPY ./shared ./shared
COPY ./services/abTestService ./services/abTestService

WORKDIR /app/services/abTestService

RUN --mount=type=cache,id=betterfly-go-mod,target=/go/pkg/mod \
    --mount=type=cache,id=betterfly-go-build,target=/root/.cache/go-build \
    go build -trimpath -o abTestMain .

FROM ubuntu:latest

WORKDIR /app

COPY --from=builder /app/services/abTestService/abTestMain .

ENTRYPOINT ["./abTestMain"]
