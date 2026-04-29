FROM golang:1.24 AS builder

ENV GOPROXY=https://goproxy.cn,direct \
    GO111MODULE=on \
    CGO_ENABLED=0

WORKDIR /app

COPY ./proto ./proto
COPY ./shared ./shared
COPY ./services/abTestService ./services/abTestService

WORKDIR /app/services/abTestService

RUN go mod tidy && \
    go build -o abTestMain .

FROM ubuntu:latest

WORKDIR /app

COPY --from=builder /app/services/abTestService/abTestMain .

ENTRYPOINT ["./abTestMain"]
