.DEFAULT_GOAL := build
BIN_FILE := app

build:
	docker compose up --remove-orphans

clean:
	docker compose down 

lint:
	golangci-lint run --enable-all
