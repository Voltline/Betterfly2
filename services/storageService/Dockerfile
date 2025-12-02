FROM golang:1.24 AS builder

ENV GOPROXY=https://goproxy.cn,direct \
    GO111MODULE=on \
    CGO_ENABLED=0

WORKDIR /app

COPY ./proto ./proto
COPY ./shared ./shared
COPY ./services/storageService ./services/storageService

WORKDIR /app/services/storageService

RUN go mod tidy && \
    go build -o storageMain .

FROM ubuntu:latest

WORKDIR /app

COPY --from=builder /app/services/storageService/storageMain .

ENTRYPOINT ["./storageMain"]