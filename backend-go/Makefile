# Creation Agent Studio — Go backend
#
# Toolchain notes (this machine): GOPROXY is set to https://goproxy.cn.
# Generated code (oapi-codegen / sqlc) is committed; reinstall the tools:
#   go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.5.0
#   go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0

OAPI  := $(shell go env GOPATH)/bin/oapi-codegen
SQLC  := $(shell go env GOPATH)/bin/sqlc

.PHONY: gen gen-api gen-db build vet test test-race migrate-up run-api run-stream run-worker run-scheduler lint

gen: gen-api gen-db

gen-api:
	$(OAPI) -config api/gen.cfg.yaml api/openapi.yaml

gen-db:
	$(SQLC) generate

build:
	go build ./...

vet:
	go vet ./...

test:
	go test ./... -count=1

test-race:
	go test ./... -race -count=1

# Applies migrations to the configured database (DB_* env or .env.local).
migrate-up:
	go run ./cmd/api -migrate

run-api:
	go run ./cmd/api

run-stream:
	go run ./cmd/stream

run-worker:
	go run ./cmd/worker

lint:
	golangci-lint run

run-scheduler:
	go run ./cmd/scheduler
