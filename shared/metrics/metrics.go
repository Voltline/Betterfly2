package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// 定义所有服务共享的指标
var (
	// 消息路由指标
	MessagesRoutedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "betterfly_messages_routed_total",
		Help: "Total number of messages routed, by route type (local, cross_container, offline)",
	}, []string{"route_type"})

	MessagesRoutingErrorsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "betterfly_messages_routing_errors_total",
		Help: "Total number of message routing errors",
	})

	MessageRoutingLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "betterfly_message_routing_latency_seconds",
		Help:    "Latency of message routing in seconds",
		Buckets: prometheus.DefBuckets,
	}, []string{"route_type"})

	// 缓存指标
	CacheHitsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "betterfly_cache_hits_total",
		Help: "Total number of cache hits, by cache_level (l1, l2)",
	}, []string{"cache_level"})

	CacheMissesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "betterfly_cache_misses_total",
		Help: "Total number of cache misses, by cache_level (l1, l2)",
	}, []string{"cache_level"})

	CacheOperationLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "betterfly_cache_operation_latency_seconds",
		Help:    "Latency of cache operations (get/set) in seconds",
		Buckets: prometheus.DefBuckets,
	}, []string{"operation", "cache_level"})

	// 数据库指标
	DatabaseQueriesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "betterfly_database_queries_total",
		Help: "Total number of database queries, by operation (select, insert, update, delete)",
	}, []string{"operation"})

	DatabaseQueryErrorsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "betterfly_database_query_errors_total",
		Help: "Total number of database query errors",
	})

	DatabaseQueryLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "betterfly_database_query_latency_seconds",
		Help:    "Latency of database queries in seconds",
		Buckets: prometheus.DefBuckets,
	})

	// Kafka指标
	KafkaMessagesConsumedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "betterfly_kafka_messages_consumed_total",
		Help: "Total number of Kafka messages consumed",
	})

	KafkaMessagesProducedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "betterfly_kafka_messages_produced_total",
		Help: "Total number of Kafka messages produced, by topic",
	}, []string{"topic"})

	KafkaProcessingErrorsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "betterfly_kafka_processing_errors_total",
		Help: "Total number of Kafka message processing errors",
	})
	KafkaDLQMessagesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "betterfly_kafka_dlq_messages_total",
		Help: "Total number of messages written to DLQ",
	}, []string{"error_class", "envelope_type"})
	KafkaDLQPublishFailuresTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "betterfly_kafka_dlq_publish_failures_total",
		Help: "Total number of DLQ publish failures",
	})
	KafkaProcessingRetriesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "betterfly_kafka_processing_retries_total",
		Help: "Total number of Kafka processing retries",
	})

	KafkaProcessingLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "betterfly_kafka_processing_latency_seconds",
		Help:    "Latency of Kafka message processing in seconds",
		Buckets: prometheus.DefBuckets,
	})
	KafkaConsumerMessagesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "betterfly_kafka_consumer_messages_total",
		Help: "Kafka consumer outcomes by service and outcome",
	}, []string{"service", "outcome"})
	KafkaConsumerRetriesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "betterfly_kafka_consumer_retries_total",
		Help: "Kafka consumer processing retries by service",
	}, []string{"service"})
	KafkaConsumerDLQMessagesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "betterfly_kafka_consumer_dlq_messages_total",
		Help: "Kafka messages written to a service DLQ",
	}, []string{"service", "error_class", "envelope_type"})
	KafkaConsumerDLQPublishFailuresTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "betterfly_kafka_consumer_dlq_publish_failures_total",
		Help: "Kafka service DLQ publication failures",
	}, []string{"service"})
	KafkaConsumerProcessingLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "betterfly_kafka_consumer_processing_seconds",
		Help:    "End-to-end Kafka message processing latency by service",
		Buckets: prometheus.DefBuckets,
	}, []string{"service"})

	PushBatchSize = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "betterfly_push_batch_size",
		Help:    "Number of APNs device tokens handled in one message batch",
		Buckets: []float64{1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 1024},
	})
	PushDeliveriesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "betterfly_push_deliveries_total",
		Help: "APNs delivery outcomes",
	}, []string{"outcome"})
	PushQueueDelay = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "betterfly_push_queue_delay_seconds",
		Help:    "Time an APNs notification waits for a bounded worker",
		Buckets: prometheus.DefBuckets,
	})
	PushAPNSLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "betterfly_push_apns_latency_seconds",
		Help:    "APNs request latency",
		Buckets: prometheus.DefBuckets,
	})
	ReliabilityCleanupRowsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "betterfly_reliability_cleanup_rows_total",
		Help: "Rows removed by bounded reliability cleanup workers",
	}, []string{"service", "kind"})
	OutboxPublishFailuresTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "betterfly_outbox_publish_failures_total",
		Help: "Outbox publication failures by service; events remain retryable",
	}, []string{"service"})
	ABTestCacheRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "betterfly_abtest_cache_requests_total",
		Help: "AB Test evaluation snapshot cache requests",
	}, []string{"outcome"})
	ABTestCacheReloadsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "betterfly_abtest_cache_reloads_total",
		Help: "AB Test evaluation snapshot reloads",
	})
	ABTestSnapshotAgeSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "betterfly_abtest_snapshot_age_seconds",
		Help: "Age of the current AB Test immutable snapshot",
	})
	ABTestDatabaseLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "betterfly_abtest_database_query_seconds",
		Help:    "AB Test evaluation database query latency",
		Buckets: prometheus.DefBuckets,
	}, []string{"query"})

	// 连接指标
	WebSocketConnectionsTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "betterfly_websocket_connections_total",
		Help: "Current number of WebSocket connections",
	})

	WebSocketConnectionsOpenedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "betterfly_websocket_connections_opened_total",
		Help: "Total number of WebSocket connections opened",
	})

	WebSocketConnectionsClosedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "betterfly_websocket_connections_closed_total",
		Help: "Total number of WebSocket connections closed",
	})

	// 用户在线状态
	OnlineUsersTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "betterfly_online_users_total",
		Help: "Current number of online users",
	})

	// HTTP请求指标
	HTTPRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "betterfly_http_requests_total",
		Help: "Total number of HTTP requests, by method, path and status",
	}, []string{"method", "path", "status"})

	HTTPRequestLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "betterfly_http_request_latency_seconds",
		Help:    "Latency of HTTP requests in seconds",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})
)

// 初始化函数，确保指标被注册
func init() {
	// 所有指标通过promauto自动注册
}
