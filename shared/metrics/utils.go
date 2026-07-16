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

// RecordKafkaMessageProduced 记录Kafka消息生产
func RecordKafkaMessageProduced(topic string) {
	KafkaMessagesProducedTotal.WithLabelValues(topic).Inc()
}

// RecordKafkaProcessingError 记录Kafka处理错误
func RecordKafkaProcessingError() {
	KafkaProcessingErrorsTotal.Inc()
}

func RecordKafkaDLQMessage(errorClass, envelopeType string) {
	KafkaDLQMessagesTotal.WithLabelValues(errorClass, envelopeType).Inc()
}

func RecordKafkaDLQPublishFailure() {
	KafkaDLQPublishFailuresTotal.Inc()
}

func RecordKafkaProcessingRetry() {
	KafkaProcessingRetriesTotal.Inc()
}

func RecordReliableConsumerOutcome(service, outcome string) {
	KafkaConsumerMessagesTotal.WithLabelValues(service, outcome).Inc()
}

func RecordReliableConsumerRetry(service string) {
	KafkaConsumerRetriesTotal.WithLabelValues(service).Inc()
}

func RecordReliableConsumerDLQ(service, errorClass, envelopeType string) {
	KafkaConsumerDLQMessagesTotal.WithLabelValues(service, errorClass, envelopeType).Inc()
}

func RecordReliableConsumerDLQFailure(service string) {
	KafkaConsumerDLQPublishFailuresTotal.WithLabelValues(service).Inc()
}

func RecordReliableConsumerLatency(service string, start time.Time) {
	KafkaConsumerProcessingLatency.WithLabelValues(service).Observe(time.Since(start).Seconds())
}

func RecordPushBatchSize(size int) {
	PushBatchSize.Observe(float64(size))
}

func RecordPushDelivery(outcome string) {
	PushDeliveriesTotal.WithLabelValues(outcome).Inc()
}

func RecordPushQueueDelay(delay time.Duration) {
	PushQueueDelay.Observe(delay.Seconds())
}

func RecordPushAPNSLatency(start time.Time) {
	PushAPNSLatency.Observe(time.Since(start).Seconds())
}

func RecordReliabilityCleanup(service, kind string, rows int64) {
	if rows > 0 {
		ReliabilityCleanupRowsTotal.WithLabelValues(service, kind).Add(float64(rows))
	}
}

func RecordOutboxPublishFailure(service string) {
	OutboxPublishFailuresTotal.WithLabelValues(service).Inc()
}

func RecordABTestCache(outcome string) {
	ABTestCacheRequestsTotal.WithLabelValues(outcome).Inc()
}

func RecordABTestCacheReload() {
	ABTestCacheReloadsTotal.Inc()
}

func SetABTestSnapshotAge(age time.Duration) {
	ABTestSnapshotAgeSeconds.Set(age.Seconds())
}

func RecordABTestDatabaseQuery(query string, start time.Time) {
	ABTestDatabaseLatency.WithLabelValues(query).Observe(time.Since(start).Seconds())
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
