# Zweep server: common tasks. Tool versions are pinned (supply chain).
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed "s/^v//" || echo dev)
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.8.0
GOSEC       := github.com/securego/gosec/v2/cmd/gosec@v2.29.0
CYCLONEDX   := github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod@v1.12.0

.PHONY: build test lint sec sbom docker check

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o dist/zweep-server ./cmd/zweep-server

# Database tests need ZWEEP_TEST_DATABASE_URL; Zabbix integration tests need ZWEEP_TEST_ZABBIX_URLS
test:
	go test -race ./...

lint:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
	go vet ./...

# gosec exclusions: G101 (token prefixes and test constants look like credentials), G104 (errors
# deliberately ignored on best-effort paths are marked with _ =), G304 (file paths come from the
# operator's configuration), G705 (false positives on structured slog calls)
sec:
	go run $(GOVULNCHECK) ./...
	go run $(GOSEC) -quiet -exclude=G101,G104,G304,G705 -exclude-dir=test ./...

# Software bill of materials (CycloneDX, JSON) of the server binary
sbom:
	mkdir -p dist
	go run $(CYCLONEDX) app -json -licenses -main cmd/zweep-server -output dist/zweep-server.sbom.json

docker:
	docker build --build-arg VERSION=$(VERSION) -t zweep-server:$(VERSION) .

check: lint test sec
