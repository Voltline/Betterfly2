module login_service

go 1.24

require (
	github.com/IBM/sarama v1.45.1
	github.com/redis/go-redis/v9 v9.7.3
	go.uber.org/zap v1.27.0
	gorm.io/driver/postgres v1.5.0
	gorm.io/gorm v1.25.12

	Betterfly2/shared v0.0.0
)

require (
	github.com/jackc/chunkreader/v2 v2.0.1 // indirect
	github.com/jackc/pgconn v1.10.1 // indirect
	github.com/jackc/pgio v1.0.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgproto3/v2 v2.2.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20200714003250-2b9c44734f2b // indirect
	github.com/jackc/pgtype v1.9.0 // indirect
	github.com/jackc/pgx/v4 v4.14.0 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	golang.org/x/crypto v0.33.0 // indirect
	golang.org/x/text v0.22.0 // indirect
)

replace Betterfly2/shared => ../../shared
