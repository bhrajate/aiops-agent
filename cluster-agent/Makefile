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

# Aggregate gate matching the project acceptance criteria.
check: fmt-check vet build test

docker:
	docker build -t aiops/cluster-agent:dev .

clean:
	rm -rf bin
