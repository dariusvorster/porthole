VERSION ?= 0.1.0

.PHONY: build release test

# Build the SPA into httpapi/dist (go:embed source), then compile the single
# portholed binary with the console embedded and the version stamped in.
build:
	npm --prefix web run build
	go build -ldflags "-X main.version=$(VERSION)" -o bin/portholed ./cmd/portholed

# Clean build + assert the version stamp landed (fails the target otherwise).
release:
	rm -rf bin
	$(MAKE) build
	@out="$$(./bin/portholed -version)"; \
	  if [ "$$out" != "porthole $(VERSION)" ]; then \
	    echo "release: version stamp mismatch: got '$$out', want 'porthole $(VERSION)'"; exit 1; \
	  fi
	@echo "release: porthole $(VERSION) built and version-stamped OK"

test:
	go test -race ./...
	npm --prefix web run test
