.PHONY: help build run test vet fmt lint tidy clean swagger docker-up docker-down

BINARY := bin/stayin-api
MAIN := ./cmd/api

help:
	@echo "Target yang tersedia:"
	@echo "  make run         jalankan server (go run)"
	@echo "  make build       kompilasi binary ke $(BINARY)"
	@echo "  make test        unit test"
	@echo "  make vet         go vet"
	@echo "  make fmt         gofmt seluruh kode"
	@echo "  make tidy        rapikan go.mod/go.sum"
	@echo "  make check       fmt + vet + test"
	@echo "  make docker-up   jalankan stack dev (postgres + backend)"
	@echo "  make docker-down hentikan stack dev"
	@echo "  make clean       hapus artefak build"

run:
	go run $(MAIN)

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BINARY) $(MAIN)

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

tidy:
	go mod tidy

check: fmt vet test

docker-up:
	docker compose -f ../POV/docker-compose.yml -f ../POV/docker-compose.build.yml up -d --build

docker-down:
	docker compose -f ../POV/docker-compose.yml -f ../POV/docker-compose.build.yml down

clean:
	rm -rf bin
