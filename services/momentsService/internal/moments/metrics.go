package moments

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"
)

type assetMetrics struct {
	db                  *gorm.DB
	count, bytes, error *prometheus.Desc
}

func RegisterMetrics(database *gorm.DB) {
	prometheus.MustRegister(&assetMetrics{database,
		prometheus.NewDesc("betterfly_moments_assets", "Number of private attachments by status", []string{"status"}, nil),
		prometheus.NewDesc("betterfly_moments_asset_bytes", "Declared private attachment bytes by status", []string{"status"}, nil),
		prometheus.NewDesc("betterfly_moments_asset_collection_error", "Attachment state collection failed", nil, nil)})
}
func (m *assetMetrics) Describe(ch chan<- *prometheus.Desc) {
	ch <- m.count
	ch <- m.bytes
	ch <- m.error
}
func (m *assetMetrics) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var rows []struct {
		Status string
		Count  int64
		Bytes  int64
	}
	err := m.db.WithContext(ctx).Raw("SELECT status, COUNT(*) AS count, COALESCE(SUM(size),0) AS bytes FROM moment_assets GROUP BY status").Scan(&rows).Error
	if err != nil {
		ch <- prometheus.MustNewConstMetric(m.error, prometheus.GaugeValue, 1)
		return
	}
	ch <- prometheus.MustNewConstMetric(m.error, prometheus.GaugeValue, 0)
	for _, status := range []string{"pending", "verified"} {
		var count, bytes int64
		for _, row := range rows {
			if row.Status == status {
				count, bytes = row.Count, row.Bytes
			}
		}
		ch <- prometheus.MustNewConstMetric(m.count, prometheus.GaugeValue, float64(count), status)
		ch <- prometheus.MustNewConstMetric(m.bytes, prometheus.GaugeValue, float64(bytes), status)
	}
}
