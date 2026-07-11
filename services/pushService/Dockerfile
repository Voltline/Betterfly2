FROM golang:1.24 AS builder

ENV GOPROXY=https://goproxy.cn,direct \
    GO111MODULE=on \
    CGO_ENABLED=0

WORKDIR /app

COPY ./proto ./proto
COPY ./shared ./shared
COPY ./services/pushService ./services/pushService

WORKDIR /app/services/pushService

RUN go mod tidy && go build -o pushMain .

FROM ubuntu:24.04

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && update-ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /app/services/pushService/pushMain .

ENTRYPOINT ["./pushMain"]
