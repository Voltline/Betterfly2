module login_service

go 1.21

require (
	go.uber.org/zap v1.27.0
	gorm.io/driver/postgres v1.5.0
	gorm.io/gorm v1.25.12

	Betterfly2/shared v0.0.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20221227161230-091c0ba34f0a // indirect
	github.com/jackc/pgx/v5 v5.3.0 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/stretchr/testify v1.10.0 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	golang.org/x/crypto v0.33.0 // indirect
	golang.org/x/text v0.22.0 // indirect
)

replace Betterfly2/shared => ../../shared
