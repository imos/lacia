# The way in for a clone of lacia, and the build of a release.
#
# make install builds the binary with the Go of this machine and hands it to
# lacia setup, which puts it in place. lacia update runs the same target again.
#
# make dist builds what a release carries: a binary for each platform and
# SHA256SUMS. The release workflow runs it on a tag; anyone can run it on a
# clean clone of the same tag and compare SHA256SUMS with the release, as
# the build depends only on the commit, the version and the Go of the
# toolchain line of go.mod.

.PHONY: install dist test

# The Go of a release. GOTOOLCHAIN makes the go command fetch it when the Go
# of this machine is another version.
TOOLCHAIN := $(shell awk '$$1 == "toolchain" { print $$2 }' go.mod)
PLATFORMS := darwin/arm64

install:
	@command -v go >/dev/null 2>&1 || { echo "Go が見つかりません。https://go.dev/dl/ から入れてから、もう1度実行してください。"; exit 1; }
	@set -e; build=$$(mktemp -d); trap 'rm -rf "$$build"' 0; \
	go build -o "$$build/lacia" ./cmd/lacia; \
	"$$build/lacia" setup $(SETUP)

dist:
	@case "$(VERSION)" in v[0-9]*) ;; *) echo "VERSION に、v0.1.0 のような版を指定してください。"; exit 1;; esac
	@test -z "$$(git status --porcelain)" || { echo "コミットしていない変更があります。リリースのビルドは、変更のないクローンで行います。"; exit 1; }
	@set -e; rm -rf dist; mkdir dist; \
	for p in $(PLATFORMS); do \
		GOTOOLCHAIN=$(TOOLCHAIN) CGO_ENABLED=0 GOOS=$${p%/*} GOARCH=$${p#*/} \
			go build -trimpath -buildvcs=true -ldflags "-X main.release=$(VERSION)" -o "dist/lacia-$${p%/*}-$${p#*/}" ./cmd/lacia; \
	done; \
	cd dist; if command -v sha256sum >/dev/null; then sha256sum lacia-*; else shasum -a 256 lacia-*; fi > SHA256SUMS; cat SHA256SUMS

test:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo "gofmt で整えていないファイルがあります。"; exit 1; }
	go vet ./...
	go test ./...
