APP     := dmetric
VERSION ?= 2026.11.5
LDFLAGS := -s -w -X dmetric.dalang.io/internal/model.Version=$(VERSION)

.PHONY: run web test build cross icon clean release release-check

# Desktop window + web app on http://127.0.0.1:8154
run:
	go run .

# Web app only (headless server)
web:
	go run . -no-window

test:
	go test ./...

# Packaged desktop apps for the current platform with the MyGo CLI
build:
	go tool mygo build

# Standalone binaries for every supported OS/arch pair
cross:
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(APP)-darwin-arm64 .
	CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(APP)-darwin-amd64 .
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(APP)-linux-arm64 .
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(APP)-linux-amd64 .
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(APP)-windows-arm64.exe .
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(APP)-windows-amd64.exe .

icon:
	go run ./tools/icongen

clean:
	rm -rf dist build .mygo

# Manual release build (binaries + macOS DMGs + SHA256SUMS + notes) into
# dist/release/v$(VERSION). Nothing is uploaded; see scripts/release.sh.
release:
	./scripts/release.sh

# Validate the release notes entry for $(VERSION) without building.
release-check:
	go run ./tools/relnotes -version $(VERSION) -check
