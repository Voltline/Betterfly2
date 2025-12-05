package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// TimeSince 记录从start时间到现在的延迟，并更新对应的Histogram指标
func TimeSince(start time.Time, observer prometheus.Observer) {
	observer.Observe(time.Since(start).Seconds())
}

// TimeSinceWithLabels 记录从start时间到现在的延迟，并更新对应的HistogramVec指标
func TimeSinceWithLabels(start time.Time, observer *prometheus.HistogramVec, labels ...string) {
	observer.WithLabelValues(labels...).Observe(time.Since(start).Seconds())
}

// IncCounter 增加计数器
func IncCounter(counter prometheus.Counter) {
	counter.Inc()
}

// IncCounterWithLabels 增加带标签的计数器
func IncCounterWithLabels(counter *prometheus.CounterVec, labels ...string) {
	counter.WithLabelValues(labels...).Inc()
}

// SetGauge 设置Gauge值
func SetGauge(gauge prometheus.Gauge, value float64) {
	gauge.Set(value)
}

// AddGauge Gauge值增加
func AddGauge(gauge prometheus.Gauge, value float64) {
	gauge.Add(value)
}

// SubGauge Gauge值减少
func SubGauge(gauge prometheus.Gauge, value float64) {
	gauge.Sub(value)
}

// RecordCacheHit 记录缓存命中
func RecordCacheHit(cacheLevel string) {
	CacheHitsTotal.WithLabelValues(cacheLevel).Inc()
}

// RecordCacheMiss 记录缓存未命中
func RecordCacheMiss(cacheLevel string) {
	CacheMissesTotal.WithLabelValues(cacheLevel).Inc()
}

// RecordCacheOperation 记录缓存操作延迟
func RecordCacheOperation(operation string, cacheLevel string, start time.Time) {
	TimeSinceWithLabels(start, CacheOperationLatency, operation, cacheLevel)
}

// RecordDatabaseQuery 记录数据库查询
func RecordDatabaseQuery(operation string, start time.Time) {
	DatabaseQueriesTotal.WithLabelValues(operation).Inc()
	TimeSince(start, DatabaseQueryLatency)
}

// RecordDatabaseError 记录数据库错误
func RecordDatabaseError() {
	DatabaseQueryErrorsTotal.Inc()
}

// RecordMessageRouted 记录消息路由
func RecordMessageRouted(routeType string, start time.Time) {
	MessagesRoutedTotal.WithLabelValues(routeType).Inc()
	TimeSinceWithLabels(start, MessageRoutingLatency, routeType)
}

// RecordRoutingError 记录路由错误
func RecordRoutingError() {
	MessagesRoutingErrorsTotal.Inc()
}

// RecordKafkaMessageConsumed 记录Kafka消息消费
func RecordKafkaMessageConsumed() {
	KafkaMessagesConsumedTotal.Inc()
}

// RecordKafkaMessageProduced 记录Kafka消息生产
func RecordKafkaMessageProduced(topic string) {
	KafkaMessagesProducedTotal.WithLabelValues(topic).Inc()
}

// RecordKafkaProcessingError 记录Kafka处理错误
func RecordKafkaProcessingError() {
	KafkaProcessingErrorsTotal.Inc()
}

// RecordKafkaProcessingLatency 记录Kafka处理延迟
func RecordKafkaProcessingLatency(start time.Time) {
	TimeSince(start, KafkaProcessingLatency)
}

// UpdateWebSocketConnections 更新WebSocket连接数
func UpdateWebSocketConnections(count int) {
	WebSocketConnectionsTotal.Set(float64(count))
}

// RecordWebSocketConnectionOpened 记录WebSocket连接打开
func RecordWebSocketConnectionOpened() {
	WebSocketConnectionsOpenedTotal.Inc()
	WebSocketConnectionsTotal.Inc()
}

// RecordWebSocketConnectionClosed 记录WebSocket连接关闭
func RecordWebSocketConnectionClosed() {
	WebSocketConnectionsClosedTotal.Inc()
	WebSocketConnectionsTotal.Dec()
}

// UpdateOnlineUsers 更新在线用户数
func UpdateOnlineUsers(count int) {
	OnlineUsersTotal.Set(float64(count))
}
