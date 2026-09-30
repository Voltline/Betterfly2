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

Only successful non-dry-run publishes mark a DLQ offset. A publish failure or
unrecoverable operation identity stops that partition without marking the failed
record or processing subsequent records. Dry-run neither publishes nor marks.

Replay preserves `operation_key` and, when present, `event_id`; changing the
Kafka partition/offset must not create a new logical business operation. Older
shared-consumer DLQs stored `event/<id>` in `operation_key`; replay restores the
corresponding `event_id`. Legacy offset-based records are reconstructed from
`original_topic`, `original_partition`, and `original_offset`. Missing/invalid
identity metadata or conflicting event/operation identities are rejected, not
silently assigned a new operation. Dry-run may still inspect such records.

Upgrade Storage/Friend/Call/Push with the updated shared consumer before using
the updated replay tool. Old consumers do not recognize the replayed
`operation_key` for offset-based operations. No new topic, schema, or client
protocol is required. Kafka headers are internal trusted metadata; keep replay
and source-topic WRITE access restricted as described above.

Replayed payloads still pass through existing Inbox, `client_message_id`, and
device-delivery ledgers within their retention windows. A completed transactional
Inbox operation is not executed again, but its replay does not recreate an
already-published response event. Replaying a DataForwarding delivery after
partial success can still duplicate a network delivery: stable `event_id` does
not by itself make that consumer deduplicate all events. Kafka and Outbox remain
at-least-once, not end-to-end exactly-once.
