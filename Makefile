.PHONY: test vet fmt build run docker clean

test:
	go test ./... -race -count=1

vet:
	go vet ./...

fmt:
	@gofmt -l . | tee /dev/stderr | grep -q . && { echo "gofmt: files need formatting"; exit 1; } || true

build:
	go build ./...

run:
	CORE_DB_PATH=/tmp/core.db go run ./cmd/core

docker:
	docker build -t xiaowen-core .

clean:
	rm -f /tmp/core.db /tmp/core.db-wal /tmp/core.db-shm
