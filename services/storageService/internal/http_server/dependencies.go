package http_server

import (
	"context"

	"Betterfly2/shared/db"
)

func pingDatabase(ctx context.Context) error {
	sqlDB, err := db.DB().DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}
