BINARY := cluster-agent
PKG    := ./cmd/cluster-agent
BIN    := bin/$(BINARY)

.PHONY: build run test vet fmt fmt-check tidy docker clean

build:
	go build -o $(BIN) $(PKG)

run:
	go run $(PKG)

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

tidy:
	go mod tidy

# 对齐项目验收标准的聚合门禁。
check: fmt-check vet build test

docker:
	docker build -t aiops/cluster-agent:dev .

clean:
	rm -rf bin
