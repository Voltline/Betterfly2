FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY ./shared ./shared
COPY ./proto ./proto
COPY ./services/paymentService ./services/paymentService

WORKDIR /app/services/paymentService

RUN go mod download && \
    go build -o paymentMain .

FROM alpine:latest

WORKDIR /app

COPY --from=builder /app/services/paymentService/paymentMain .

EXPOSE 8084

ENTRYPOINT ["./paymentMain"]
