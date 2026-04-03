FROM golang:1.24 AS builder

ENV GOPROXY=https://goproxy.cn,direct \
    GO111MODULE=on \
    CGO_ENABLED=0

WORKDIR /app

COPY ./proto ./proto
COPY ./shared ./shared
COPY ./services/friendService ./services/friendService

WORKDIR /app/services/friendService

RUN go mod tidy && \
    go build -o friendMain .

FROM ubuntu:latest

WORKDIR /app

COPY --from=builder /app/services/friendService/friendMain .

ENTRYPOINT ["./friendMain"]
