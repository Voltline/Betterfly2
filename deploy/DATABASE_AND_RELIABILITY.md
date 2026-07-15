# Database and Kafka Reliability Operations

## Schema migrations

Business services do not execute DDL by default. They connect with their runtime
database role and verify that `schema_migrations` is at least the version compiled
into the service. Run migrations with a separate role that has DDL permission:

```bash
docker compose -f services/docker-compose.yml run --rm db_migrate
```

For Kubernetes, publish `betterfly2/db-migrate:latest`, apply the migration Job,
and wait for it before rolling business Deployments:

```bash
kubectl apply -f deploy/k8s/base/namespace.yaml
kubectl apply -f deploy/k8s/base/configmap.yaml -f deploy/k8s/base/secret.example.yaml
kubectl apply -f deploy/k8s/base/infrastructure-jobs.yaml
kubectl -n betterfly2 wait --for=condition=complete job/betterfly-db-migrate --timeout=5m
kubectl -n betterfly2 wait --for=condition=complete job/betterfly-kafka-topics --timeout=5m
kubectl apply -k deploy/k8s/base
```

`DB_AUTO_MIGRATE=true` remains available for a single-process development setup.
It is disabled by default and must not be enabled on production business Pods.
`DB_SCHEMA_CHECK=true` is the production default.

## Connection budget

Each process accepts these validated settings:

- `DB_MAX_OPEN_CONNS` defaults to `50`.
- `DB_MAX_IDLE_CONNS` defaults to `10` and cannot exceed max open.
- `DB_CONN_MAX_LIFETIME` defaults to `1h`.
- `DB_CONN_MAX_IDLE_TIME` defaults to `10m` and cannot exceed max lifetime.

Budget PostgreSQL connections before scaling:

```text
total runtime budget = sum(DB_MAX_OPEN_CONNS per service replica)
required server capacity >= runtime budget + migration/admin reserve
```

For example, 2 Storage, 2 Friend, 2 Push, 2 AB Test, 2 DataForwarding and 1 Auth
replica at 20 connections each require up to 220 runtime connections. Keep an
additional reserve for migrations, maintenance and failover. Prometheus exports
`betterfly_db_*` pool stats from each service `/metrics` endpoint.

## Kafka retry and DLQ operations

Storage, Friend, Call and Push each own a DLQ:

- `storage-service-dlq`
- `friend-service-dlq`
- `call-service-dlq`
- `push-service-dlq`

Compose and Kubernetes configure seven-day deletion retention. A source offset is
committed only after successful processing, or after a permanent/exhausted message
is successfully written to its DLQ. A failed DLQ write blocks the partition and
prevents committing later offsets. When Kafka authorization is enabled, grant each
service principal `WRITE` and `DESCRIBE` only on its own DLQ; local PLAINTEXT mode
does not enforce ACLs.

DLQ payloads retain the original bytes and source coordinates for controlled replay.
Ordinary logs contain only a bounded error summary and identifiers, not message
contents, JWTs or tokens. Alerts fire for DLQ entries and DLQ publication failures.

Each service accepts `<SERVICE>_KAFKA_PROCESS_MAX_RETRIES` (default `3`),
`<SERVICE>_KAFKA_RETRY_INITIAL_BACKOFF` (default `100ms`) and
`<SERVICE>_KAFKA_RETRY_MAX_BACKOFF` (default `2s`). `KAFKA_NETWORK_TIMEOUT`
defaults to `10s` for all synchronous producers. A transient error may expose a
longer retry boundary, such as an active Push delivery lease; that boundary is
honored without advancing the partition and still counts toward the finite retry
limit.

`PUSH_APNS_MAX_CONCURRENCY` defaults to `16` and `PUSH_DELIVERY_LEASE` defaults
to `30s`. Ordinary message pushes require a positive `message_id`; the
`message_id + token_id` ledger prevents already-sent devices from being retried.
Invalid-token deactivation and the corresponding `permanent` ledger transition
are committed in one database transaction.

`KAFKA_ACL_ENABLED=true` makes the Compose topic job install service-scoped
`WRITE` and `DESCRIBE` grants for the configured principals. This flag is useful
only when the Kafka cluster already has authenticated principals and an authorizer
enabled; otherwise local PLAINTEXT remains intentionally unenforced.
