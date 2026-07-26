GOPROXY ?= https://goproxy.cn,direct
export GOPROXY

.PHONY: build run test vet fmt tidy docker

build:
	go build -o bin/control-plane ./cmd/control-plane

run:
	go run ./cmd/control-plane

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

tidy:
	go mod tidy

docker:
	docker build -t aiops/control-plane:dev .
