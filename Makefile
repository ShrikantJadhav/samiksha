BIN := samiksha

.PHONY: build test vet fmt run clean

build:
	go build -o $(BIN) ./cmd/samiksha

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

clean:
	rm -f $(BIN)
