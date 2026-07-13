# Data Forwarding DLQ

`data-forwarding-dlq` contains original Kafka payload bytes and therefore may
contain message content or authentication material. Restrict consume access to
the operations/replay principal and produce access to dataForwardingService.

The Compose deployment creates and configures this topic through the one-shot
`kafka-init` service before either DataForwarding container starts. Its defaults
are three partitions, replication factor 2, `min.insync.replicas=1`, and seven
days of retention. They can be changed with:

```text
DF_KAFKA_DLQ_PARTITIONS
DF_KAFKA_DLQ_REPLICATION_FACTOR
DF_KAFKA_DLQ_MIN_ISR
DF_KAFKA_DLQ_RETENTION_MS
```

The Kubernetes development base performs the same idempotent initialization in
the DataForwarding init container. Because that base deploys one Kafka broker,
its replication factor and minimum ISR are both 1.

Manual development equivalent:

```bash
kafka-topics --bootstrap-server kafka1:9092 --create --if-not-exists \
  --topic data-forwarding-dlq --partitions 3 --replication-factor 2 \
  --config cleanup.policy=delete --config retention.ms=604800000 \
  --config min.insync.replicas=1
```

Production recommendation: replication factor 3, `min.insync.replicas=2`, a
7-day retention (adjust to incident response requirements), encryption in
transit, and ACLs equivalent to:

```text
ALLOW data-forwarding principal WRITE data-forwarding-dlq
ALLOW dlq-replay principal READ data-forwarding-dlq
ALLOW dlq-replay principal WRITE only explicitly approved original topics
DENY other principals READ data-forwarding-dlq
```

The repository's current Kafka listeners use unauthenticated PLAINTEXT, so they
cannot enforce principal-specific ACLs. Configure SASL/mTLS identities before
enabling the production ACL policy; broad ACLs for `User:ANONYMOUS` do not
protect DLQ contents.

Replay defaults to dry-run and requires an original-topic allowlist:

```bash
go run ./tools/dlq-replay -allow-topics=<pod-topic>,storage-service -max=100
go run ./tools/dlq-replay -dry-run=false -allow-topics=<pod-topic> -max=20
```

Only successful non-dry-run publishes commit a DLQ offset. Replayed business
payloads still pass through the normal `client_message_id` and side-effect
idempotency mechanisms.
