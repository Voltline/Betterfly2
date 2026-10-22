package moments

import (
	"Betterfly2/shared/logger"
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// UnattachedRetention 之后，未发布的图片（含未完成上传）连同对象一起删除。
	// 上传URL只有10分钟，正常发布都在这之内完成；更晚重试的冻结草稿会得到409，客户端解锁后重新上传。
	UnattachedRetention = 24 * time.Hour
	cleanupInterval     = time.Hour
	cleanupBatch        = 100
	cleanupMaxBatches   = 50
)

type CleanupStats struct {
	Unattached, DeletedPosts, Staging int
}

// RunCleanup 每小时清理一次，直到ctx结束。多副本同时执行时靠SKIP LOCKED分摊，删除幂等。
func (s *Service) RunCleanup(ctx context.Context) {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for {
		stats, err := s.Cleanup(ctx, now())
		if err != nil && ctx.Err() == nil {
			logger.Sugar().Warnw("朋友圈存储清理未完成", "error", err)
		}
		if stats != (CleanupStats{}) {
			logger.Sugar().Infow("朋友圈存储清理", "unattached", stats.Unattached, "deleted_posts", stats.DeletedPosts, "staging", stats.Staging)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Cleanup 删除过期未发布图片、已删除动态的图片以及遗留的暂存对象。
func (s *Service) Cleanup(ctx context.Context, at time.Time) (CleanupStats, error) {
	var stats CleanupStats
	var err error
	cutoff := at.Add(-UnattachedRetention)
	if stats.Unattached, err = s.purgeAssets(ctx, func(q *gorm.DB) *gorm.DB {
		return q.Where("post_id IS NULL AND created_at < ?", cutoff)
	}); err != nil {
		return stats, err
	}
	if stats.DeletedPosts, err = s.purgeAssets(ctx, func(q *gorm.DB) *gorm.DB {
		return q.Where("post_id IN (SELECT id FROM moment_posts WHERE deleted_at IS NOT NULL)")
	}); err != nil {
		return stats, err
	}
	stats.Staging, err = s.media.DeleteStagingBefore(ctx, cutoff)
	return stats, err
}

// purgeAssets 在行锁内先删对象再删行：删对象失败就回滚，下次重试；
// 发布动态会锁住同一行，等到这里提交后发现图片已不存在，不会引用被删的对象。
func (s *Service) purgeAssets(ctx context.Context, scope func(*gorm.DB) *gorm.DB) (int, error) {
	total := 0
	for range cleanupMaxBatches {
		count := 0
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var rows []struct {
				ID                     int64
				StagingKey, StorageKey string
			}
			query := scope(tx.Table("moment_assets").Select("id", "staging_key", "storage_key")).
				Order("id").Limit(cleanupBatch).Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
			if err := query.Find(&rows).Error; err != nil || len(rows) == 0 {
				return err
			}
			ids := make([]int64, len(rows))
			keys := make([]string, 0, len(rows)*2)
			for i, row := range rows {
				ids[i] = row.ID
				keys = append(keys, row.StagingKey, row.StorageKey)
			}
			if err := s.media.Delete(ctx, keys); err != nil {
				return err
			}
			if err := tx.Exec("DELETE FROM moment_assets WHERE id IN ?", ids).Error; err != nil {
				return err
			}
			count = len(rows)
			return nil
		})
		total += count
		if err != nil || count < cleanupBatch {
			return total, err
		}
	}
	return total, nil
}
