DB_DSN ?= postgres://dev:devpass@localhost:5432/homeservice?sslmode=disable

migrate:
	go run ./cmd/migrate -cmd=up -dsn="$(DB_DSN)"

migrate-down:
	go run ./cmd/migrate -cmd=down -dsn="$(DB_DSN)"

migrate-reset:
	go run ./cmd/migrate -cmd=reset -dsn="$(DB_DSN)"

migrate-status:
	go run ./cmd/migrate -cmd=status -dsn="$(DB_DSN)"

seed:
	go run ./cmd/seed -dsn="$(DB_DSN)"

fresh:
	go run ./cmd/migrate -cmd=reset -dsn="$(DB_DSN)"
	go run ./cmd/migrate -cmd=up -dsn="$(DB_DSN)"
	go run ./cmd/seed -dsn="$(DB_DSN)"

run:
	go run ./cmd/api

# Everyday start: apply any new migrations (does nothing when there are none),
# then run the API. Demo cameras and appliances unless told otherwise:
#   make dev CAMERAS_DRIVER=      (empty = use the Imou account, if configured)
#   make dev APPLIANCES_DRIVER=   (empty = appliances off)
#   make dev APPLIANCES_DRIVER=fake   (a fixed demo set instead of your own)
CAMERAS_DRIVER ?= fake
APPLIANCES_DRIVER ?= local
dev:
	go run ./cmd/migrate -cmd=up
	CAMERAS_DRIVER=$(CAMERAS_DRIVER) APPLIANCES_DRIVER=$(APPLIANCES_DRIVER) go run ./cmd/api

# Sample bills and purchases so the spending dashboard has something to draw.
# Every row is marked "(demo)"; demo-spending-clear removes only those rows.
demo-spending:
	go run ./cmd/demospend -dsn="$(DB_DSN)"

demo-spending-clear:
	go run ./cmd/demospend -dsn="$(DB_DSN)" -clear
