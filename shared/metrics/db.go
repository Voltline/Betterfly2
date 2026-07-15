package metrics

import (
	"database/sql"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

var registerDBStatsOnce sync.Once

func RegisterDBStats(database *sql.DB) {
	registerDBStatsOnce.Do(func() {
		prometheus.MustRegister(&dbStatsCollector{
			database: database,
			maxOpen:  prometheus.NewDesc("betterfly_db_max_open_connections", "Configured maximum database connections", nil, nil),
			open:     prometheus.NewDesc("betterfly_db_open_connections", "Open database connections", nil, nil),
			inUse:    prometheus.NewDesc("betterfly_db_in_use_connections", "Database connections currently in use", nil, nil),
			idle:     prometheus.NewDesc("betterfly_db_idle_connections", "Idle database connections", nil, nil),
			waits:    prometheus.NewDesc("betterfly_db_wait_count_total", "Database connection waits", nil, nil),
			waitTime: prometheus.NewDesc("betterfly_db_wait_duration_seconds_total", "Time spent waiting for database connections", nil, nil),
		})
	})
}

type dbStatsCollector struct {
	database           *sql.DB
	maxOpen, open      *prometheus.Desc
	inUse, idle, waits *prometheus.Desc
	waitTime           *prometheus.Desc
}

func (c *dbStatsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.maxOpen
	ch <- c.open
	ch <- c.inUse
	ch <- c.idle
	ch <- c.waits
	ch <- c.waitTime
}

func (c *dbStatsCollector) Collect(ch chan<- prometheus.Metric) {
	stats := c.database.Stats()
	ch <- prometheus.MustNewConstMetric(c.maxOpen, prometheus.GaugeValue, float64(stats.MaxOpenConnections))
	ch <- prometheus.MustNewConstMetric(c.open, prometheus.GaugeValue, float64(stats.OpenConnections))
	ch <- prometheus.MustNewConstMetric(c.inUse, prometheus.GaugeValue, float64(stats.InUse))
	ch <- prometheus.MustNewConstMetric(c.idle, prometheus.GaugeValue, float64(stats.Idle))
	ch <- prometheus.MustNewConstMetric(c.waits, prometheus.CounterValue, float64(stats.WaitCount))
	ch <- prometheus.MustNewConstMetric(c.waitTime, prometheus.CounterValue, stats.WaitDuration.Seconds())
}
