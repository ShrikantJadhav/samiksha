BIN := viveka

.PHONY: build test vet fmt run clean

build:
	go build -o $(BIN) ./cmd/viveka

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

clean:
	rm -f $(BIN)
