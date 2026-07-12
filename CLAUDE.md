# Repository Guidance

Betterfly2 is a Go 1.24 microservice backend for an instant-messaging client. Treat the root [README](README.md) as the current architecture and documentation index; do not infer the active service list from the historical architecture image in `others/`.

## Active services

- `dataForwardingService`: TLS WebSocket gateway, authenticated dispatch and Kafka/Redis routing.
- `authService`: synchronous gRPC registration, login and JWT lifecycle.
- `storageService`: message/profile persistence, sync queries, cache and RustFS HTTP control plane.
- `friendService`: Kafka worker for contacts, groups and memberships.
- `abTestService`: HTTP experiment evaluation and protected administration UI.
- `callService`: Kafka/Redis one-to-one WebRTC signaling state machine.
- `pushService`: APNs/PushKit token storage, notification delivery and protected administration UI.

## Development

The repository contains multiple Go modules. Run tests from each affected module rather than expecting root `go test ./...` to cover the repository.

```bash
cd services
./deploy_docker_compose.sh standard --cert

# Rebuild only changed services during development.
./rebuild_docker_compose.sh --proto df storage
```

`PGSQL_DSN` points to an external PostgreSQL instance. The standard Compose preset enables all product features; direct `docker compose up -d` or the `minimal` preset does not start Storage Service and therefore is not a complete messaging environment.

## Communication boundaries

- Clients send Protobuf requests over `/ws`; JWT is carried by the protocol request, not an HTTP Authorization header.
- Auth calls are synchronous gRPC.
- Storage, friend, call and push work is carried over Kafka topics named `storage-service`, `friend-service`, `call-service` and `push-service`.
- DataForwarding Pod topics route asynchronous responses back to the correct WebSocket owner.
- Redis stores cross-pod routes, shared cache entries and active call state.
- WebRTC media and RustFS object bytes bypass application services.

## Change workflow

For a new protocol endpoint, follow [INTERFACE_DEVELOPMENT.md](INTERFACE_DEVELOPMENT.md). Keep payload registration in the owning module, regenerate Protobuf with `make -C proto`, and add tests in the affected module. Cross-service friend/group flows are documented in [REGRESSION_TESTING.md](REGRESSION_TESTING.md).

Never commit `.env`, APNs `.p8` keys, generated private keys or production credentials.
