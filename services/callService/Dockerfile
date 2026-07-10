FROM golang:1.24 AS builder

ENV GOPROXY=https://goproxy.cn,direct \
    GO111MODULE=on \
    CGO_ENABLED=0

WORKDIR /app

COPY ./proto ./proto
COPY ./shared ./shared
COPY ./services/callService ./services/callService

WORKDIR /app/services/callService

RUN go mod tidy && go build -o callMain .

FROM ubuntu:latest

WORKDIR /app

COPY --from=builder /app/services/callService/callMain .

ENTRYPOINT ["./callMain"]
