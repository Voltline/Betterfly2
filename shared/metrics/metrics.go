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

	KafkaProcessingLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "betterfly_kafka_processing_latency_seconds",
		Help:    "Latency of Kafka message processing in seconds",
		Buckets: prometheus.DefBuckets,
	})

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
