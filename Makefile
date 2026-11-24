BIN := samiksha

.PHONY: build test vet fmt run clean eval

build:
	go build -o $(BIN) ./cmd/samiksha

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

eval:
	sh scripts/eval.sh $(ARGS)

clean:
	rm -f $(BIN)
