# Betterfly2 Kubernetes Manifests

This is a first-pass Kubernetes deployment for local or single-cluster testing.
It mirrors the current `docker-compose.yml` topology closely, while keeping the
manifests small enough to evolve into multi-region overlays later.

## Scope

Included:

- Redis
- Single-node Kafka for development testing
- RustFS with a PVC
- `authService`
- `dataForwardingService`
- `storageService`
- `friendService`
- `callService` and a Coturn relay
- Optional nginx Ingress routes for `/ws` and `/storage_service`

Not production-ready yet:

- Kafka should move to Strimzi, Bitnami Helm, or a managed Kafka service.
- PostgreSQL is expected to be external through `PGSQL_DSN`.
- RustFS is single-replica in this base.
- Ingress host and TLS settings are placeholders.

## Build Local Images

From the repository root:

```bash
docker build -t betterfly2/auth-service:latest -f services/authService/Dockerfile .
docker build -t betterfly2/data-forwarding-service:latest -f services/dataForwardingService/Dockerfile .
docker build -t betterfly2/storage-service:latest -f services/storageService/Dockerfile .
docker build -t betterfly2/friend-service:latest -f services/friendService/Dockerfile .
docker build -t betterfly2/call-service:latest -f services/callService/Dockerfile .
```

For `kind`, load the images into the cluster:

```bash
kind load docker-image betterfly2/auth-service:latest
kind load docker-image betterfly2/data-forwarding-service:latest
kind load docker-image betterfly2/storage-service:latest
kind load docker-image betterfly2/friend-service:latest
kind load docker-image betterfly2/call-service:latest
```

For a remote cluster, push these images to a registry and update the image names
in `base/*.yaml` or with a Kustomize overlay.

## Configure Secrets

Copy the example secret and replace placeholders:

```bash
cp deploy/k8s/base/secret.example.yaml /tmp/betterfly2-secret.yaml
```

Edit `/tmp/betterfly2-secret.yaml`, especially `PGSQL_DSN`, then apply it with
the rest of the base manifests.

For WebRTC, also replace `TURN_SHARED_SECRET` and set `TURN_PUBLIC_HOST` plus
`TURN_EXTERNAL_IP` in `base/configmap.yaml`. Coturn uses `hostNetwork`, so the
selected node must have a stable public IP and allow `3478/tcp`, `3478/udp`,
and `49160-49200/udp` through its firewall or security group.

## Deploy

```bash
kubectl apply -k deploy/k8s/base
```

Check rollout status:

```bash
kubectl -n betterfly2 get pods
kubectl -n betterfly2 get svc
kubectl -n betterfly2 logs deploy/data-forwarding
```

## Local Access

Without Ingress, port-forward the services:

```bash
kubectl -n betterfly2 port-forward svc/data-forwarding 54342:54342
kubectl -n betterfly2 port-forward svc/storage-service 8081:8081
kubectl -n betterfly2 port-forward svc/rustfs 9000:9000
```

Then use:

- WebSocket: `wss://localhost:54342/ws`
- Storage HTTP: `http://localhost:8081/storage_service`
- RustFS S3 API: `http://localhost:9000`

## Notes

`dataForwardingService` currently serves WebSocket over TLS itself. The nginx
Ingress route for `/ws` therefore uses an HTTPS backend. The storage service is
plain HTTP, so it has a separate Ingress object.
