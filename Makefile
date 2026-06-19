BINARY  := akita
CMD     := ./cmd/akita
GOFLAGS := -trimpath
LDFLAGS := -s -w

.PHONY: build test lint vet vuln clean docker

build:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BINARY) $(CMD)

test:
	go test -race ./...

lint:
	golangci-lint run

vet:
	go vet ./...

vuln:
	govulncheck ./...

clean:
	rm -f $(BINARY)

docker:
	docker build -t $(BINARY) .
