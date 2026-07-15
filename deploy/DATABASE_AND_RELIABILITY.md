# Database and Kafka Reliability Operations

## Schema migrations

Business services do not execute DDL by default. They connect with their runtime
database role and verify that `schema_migrations` is at least the version compiled
into the service. Run migrations with a separate role that has DDL permission:

```bash
docker compose -f services/docker-compose.yml run --rm db_migrate
```

Migrations are an explicit ordered list. Each version is committed to
`schema_migrations` only after that version succeeds, and one PostgreSQL session
advisory lock covers the complete migration run. For schema v4, publish the
immutable `betterfly2/db-migrate:schema-v4` image (prefer a digest in production),
run the versioned Job, and wait for it before rolling business Deployments:

```bash
kubectl apply -f deploy/k8s/base/namespace.yaml
kubectl apply -f deploy/k8s/base/configmap.yaml -f /tmp/betterfly2-secret.yaml
kubectl apply -k deploy/k8s/migrations
kubectl -n betterfly2 wait --for=condition=complete job/betterfly-db-migrate-v4 --timeout=5m
kubectl apply -k deploy/k8s/base
kubectl -n betterfly2 wait --for=condition=complete job/betterfly-kafka-topics --timeout=5m
```

The pre-v4 migrator recorded only its final snapshot marker. When an existing
database contains only `version=3`, the v4 runner atomically normalizes the
attested legacy snapshot to ledger rows `1,2,3` under the same advisory lock,
then runs migration 4. Gaps involving version 4 or newer remain fatal instead of
being guessed or silently backfilled.

The migration Job is deliberately absent from the normal base kustomization.
Its versioned name makes every schema release execute once. The included Argo CD
`PreSync` annotation makes migration success a Deployment rollout prerequisite;
other deployment systems must implement the same ordered gate explicitly.

`DB_AUTO_MIGRATE=true` remains available for a single-process development setup.
It is disabled by default and must not be enabled on production business Pods.
`DB_SCHEMA_CHECK=true` is the production default.

## Connection budget

Each process accepts these validated settings. Go defaults remain useful for
development, while Kubernetes overrides `DB_MAX_OPEN_CONNS` per Deployment:

- `DB_MAX_OPEN_CONNS` defaults to `50`.
- `DB_MAX_IDLE_CONNS` defaults to `10` and cannot exceed max open.
- `DB_CONN_MAX_LIFETIME` defaults to `1h`.
- `DB_CONN_MAX_IDLE_TIME` defaults to `10m` and cannot exceed max lifetime.

Budget PostgreSQL connections before scaling:

```text
total runtime budget = sum(DB_MAX_OPEN_CONNS per service replica)
required server capacity >= runtime budget + migration/admin reserve
```

The base manifests currently budget 94 runtime connections: DataForwarding 24,
Push 24, AB Test 16, Storage 12, Friend 10 and Auth 8. Reserve at least another
4 connections for the migration Job plus capacity for administration and failover.
Prometheus exports `betterfly_db_*` pool stats from each service `/metrics` endpoint.

Production may place PgBouncer in transaction-pooling mode in front of PostgreSQL
by pointing `PGSQL_DSN` at PgBouncer. Keep the application pool bounds in place:
PgBouncer reduces server backends, but does not make unbounded client pools safe.

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

Storage, Friend and Push commit their inbox marker, business writes and outbox
events in one PostgreSQL transaction. The source offset can be acknowledged after
that transaction, while a separate leased relay publishes stable `event_id` values.
Publishing is at-least-once: downstream consumers deduplicate the stable event ID,
and a crash after Kafka accepts an event but before the relay records `published`
may cause the same event to be sent again.
Outbox publication failures remain `retryable` indefinitely with capped exponential
backoff; `OUTBOX_ALERT_AFTER_ATTEMPTS` defaults to `20` and controls structured
alerts, not data deletion. A service may override it with, for example,
`STORAGE_OUTBOX_ALERT_AFTER_ATTEMPTS`. `betterfly_outbox_publish_failures_total`
tracks failures using only the bounded service label.

Call state transitions append their logical events to a Redis Stream in the same
Lua operation. The relay deletes entries only after publication is recorded; it
does not trim unresolved entries by length, so an extended Kafka outage creates
observable Redis backlog rather than silently dropping pending call or VoIP events.
`CALL_OPERATION_RETENTION` and `CALL_OUTBOX_SENT_RETENTION` default to `720h`
and are forced beyond `KAFKA_MAX_REPLAY_WINDOW`, matching the PostgreSQL inbox
retention rule.

Push Kafka consumers persist jobs and per-token rows without waiting for APNs.
Workers reclaim `pending`, due `retryable`, or lease-expired `claimed` rows in
bounded batches. Finalization is fenced by token and attempt, so an expired worker
cannot overwrite its replacement. `PUSH_DELIVERY_RETENTION` defaults to `720h`,
`PUSH_CLEANUP_INTERVAL` to `1h`, and `PUSH_CLEANUP_BATCH_SIZE` to `1000`.

Completed inbox, legacy result and outbox rows are also cleaned by a limited
background task. `CONSUMER_STATE_RETENTION` defaults to `720h` and is always
forced beyond `KAFKA_MAX_REPLAY_WINDOW` (default `168h`). The cleanup interval
and batch default to `1h` and `1000`; deleted rows are logged and exported through
`betterfly_reliability_cleanup_rows_total`.

`KAFKA_ACL_ENABLED=true` makes the Compose topic job install service-scoped
`WRITE` and `DESCRIBE` grants for the configured principals. This flag is useful
only when the Kafka cluster already has authenticated principals and an authorizer
enabled; otherwise local PLAINTEXT remains intentionally unenforced.
