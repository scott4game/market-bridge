.PHONY: test build run-server run-client docker-server docker-client docker compose-server compose-local

test:
	go test ./...

build:
	go build ./cmd/go-server ./cmd/go-client

run-server:
	GO_SERVER_PROVIDER=mock go run ./cmd/go-server

run-client:
	go run ./cmd/go-client serve

docker-server:
	docker build --target go-server -t massive-go-server:local .

docker-client:
	docker build --target go-client -t massive-go-client:local .

docker: docker-server docker-client

compose-server:
	docker compose --profile server up --build

compose-local:
	docker compose --profile local up --build
