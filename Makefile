VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build dist test install deploy clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/ego-bridge .

test:
	go vet ./... && go test ./...

# Static binaries for the remote servers.
dist:
	GOOS=linux  GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o dist/ego-bridge-linux-amd64 .
	GOOS=linux  GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o dist/ego-bridge-linux-arm64 .
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o dist/ego-bridge-darwin-arm64 .
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o dist/ego-bridge-darwin-amd64 .

# Install on this computer.
install: build
	install -m 0755 bin/ego-bridge $(HOME)/.local/bin/ego-bridge

# Copy to a server and set it up: make deploy HOST=myserver [ARCH=arm64]
ARCH ?= amd64
deploy: dist
	ssh $(HOST) 'mkdir -p ~/.local/bin'
	scp dist/ego-bridge-linux-$(ARCH) $(HOST):.local/bin/ego-bridge
	ssh $(HOST) '~/.local/bin/ego-bridge install'

clean:
	rm -rf bin dist
