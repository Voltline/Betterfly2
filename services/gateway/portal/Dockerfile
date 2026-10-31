FROM golang:1.25 AS builder

ENV CGO_ENABLED=0

WORKDIR /src
COPY go.mod *.go index.html ./
RUN go build -trimpath -ldflags="-s -w" -o /portal .

FROM scratch

COPY --from=builder /portal /portal

EXPOSE 8080
ENTRYPOINT ["/portal"]
