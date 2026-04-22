VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

MODULE := github.com/relayra/relayra
BINARY := relayra
BUILD_DIR := build
DIST_DIR := dist

LDFLAGS := -s -w \
	-X '$(MODULE)/internal/cli.Version=$(VERSION)' \
	-X '$(MODULE)/internal/cli.BuildDate=$(BUILD_DATE)'

GOOS := linux
GOARCH := amd64

.PHONY: all build clean dist

all: build

build:
	@echo "Building $(BINARY) $(VERSION) for $(GOOS)/$(GOARCH)..."
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) ./cmd/relayra

clean:
	rm -rf $(BUILD_DIR) $(DIST_DIR)

dist: build
	@echo "Creating distribution archive..."
	@mkdir -p $(DIST_DIR)/$(BINARY)-$(VERSION)
	@cp $(BUILD_DIR)/$(BINARY) $(DIST_DIR)/$(BINARY)-$(VERSION)/
	@cp scripts/install.sh $(DIST_DIR)/$(BINARY)-$(VERSION)/
	@chmod +x $(DIST_DIR)/$(BINARY)-$(VERSION)/install.sh
	@cp GUIDE.md $(DIST_DIR)/$(BINARY)-$(VERSION)/
	@cd $(DIST_DIR) && tar czf $(BINARY)-$(VERSION)-linux-amd64.tar.gz $(BINARY)-$(VERSION)/
	@rm -rf $(DIST_DIR)/$(BINARY)-$(VERSION)
	@echo "Archive: $(DIST_DIR)/$(BINARY)-$(VERSION)-linux-amd64.tar.gz"

fmt:
	go fmt ./...

vet:
	go vet ./...

test:
	go test -v ./...

tidy:
	go mod tidy
