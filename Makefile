-include .release.env

BINARY := teslamate-dawarich
PLATFORMS ?= linux/amd64,linux/arm64,linux/arm/v7
DOCKERHUB_REPO ?= lukstankovic/teslamate-dawarich
GHCR_REPO ?= ghcr.io/lukstankovic/teslamate-dawarich
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || true)
VERSION := $(if $(strip $(VERSION)),$(VERSION),dev)
CURRENT_VERSION := $(shell git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//')

.PHONY: build test lint fmt tidy binary docker-image images \
        version release release-patch release-minor release-major \
        changelog dockerhub-description clean

# --- development --------------------------------------------------------------

## build: compile the binary, version stamped
build:
	go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BINARY) ./cmd/$(BINARY)

## test: run the tests with the race detector
test:
	go test -race ./...

## lint: run golangci-lint
lint:
	golangci-lint run

## fmt: format every package
fmt:
	gofmt -l -w .

## tidy: prune and sync go.mod
tidy:
	go mod tidy

## docker-image: local single-arch image (no push)
docker-image:
	docker build --build-arg VERSION=$(VERSION) -t $(BINARY):dev .

## clean: remove build output
clean:
	rm -f $(BINARY)

# --- releasing ----------------------------------------------------------------
# Tagging is the trigger: the release workflow builds and pushes the multi-arch
# image for the tag. `make images` is only for pushing by hand.

## version: print the current tag and the next patch/minor/major versions
version:
	@cur="$(CURRENT_VERSION)"; [ -n "$$cur" ] || cur="0.0.0"; \
	echo "current:    $$cur"; \
	echo "next patch: $$(echo $$cur | awk -F. '{printf "%d.%d.%d", $$1,$$2,$$3+1}')"; \
	echo "next minor: $$(echo $$cur | awk -F. '{printf "%d.%d.0", $$1,$$2+1}')"; \
	echo "next major: $$(echo $$cur | awk -F. '{printf "%d.0.0", $$1+1}')"

## release: test, tag, push, create the GitHub release, sync CHANGELOG.md.
## Usage: make release VERSION=1.0.0 [NOTES="..." | NOTES_FILE=highlights.md]
release:
	@test -n "$(VERSION)" || (echo "VERSION required: make release VERSION=1.0.0"; exit 1)
	@git diff --quiet || (echo "working tree dirty — commit first"; exit 1)
	$(MAKE) test
	git tag v$(VERSION)
	git push origin v$(VERSION)
	if [ -n "$(NOTES_FILE)" ]; then \
		gh release create v$(VERSION) --title "v$(VERSION)" --notes-file "$(NOTES_FILE)" --generate-notes; \
	elif [ -n "$(NOTES)" ]; then \
		gh release create v$(VERSION) --title "v$(VERSION)" --notes "$(NOTES)" --generate-notes; \
	else \
		gh release create v$(VERSION) --title "v$(VERSION)" --generate-notes; \
	fi
	$(MAKE) changelog VERSION=$(VERSION)
	@echo "released v$(VERSION)"

## release-patch: bugfix bump (x.y.Z+1) then release
release-patch:
	@cur="$(CURRENT_VERSION)"; [ -n "$$cur" ] || cur="0.0.0"; \
	$(MAKE) release VERSION=$$(echo $$cur | awk -F. '{printf "%d.%d.%d", $$1,$$2,$$3+1}')

## release-minor: feature bump (x.Y+1.0) then release
release-minor:
	@cur="$(CURRENT_VERSION)"; [ -n "$$cur" ] || cur="0.0.0"; \
	$(MAKE) release VERSION=$$(echo $$cur | awk -F. '{printf "%d.%d.0", $$1,$$2+1}')

## release-major: breaking bump (X+1.0.0) then release
release-major:
	@cur="$(CURRENT_VERSION)"; [ -n "$$cur" ] || cur="0.0.0"; \
	$(MAKE) release VERSION=$$(echo $$cur | awk -F. '{printf "%d.0.0", $$1+1}')

## changelog: append the GitHub release notes for VERSION to CHANGELOG.md and push
changelog:
	@test -n "$(VERSION)" || (echo "VERSION required"; exit 1)
	tmp=$$(mktemp); \
	gh release view v$(VERSION) --json body,publishedAt -q '"## v$(VERSION) — " + (.publishedAt | split("T")[0]) + "\n\n" + .body + "\n"' > $$tmp; \
	awk -v f="$$tmp" '{print} /new releases inserted below this line/{print ""; while((getline l < f) > 0) print l}' CHANGELOG.md > CHANGELOG.md.tmp && mv CHANGELOG.md.tmp CHANGELOG.md; \
	rm -f $$tmp
	git add CHANGELOG.md
	git commit -m "docs: changelog for v$(VERSION)"
	git push
	@echo "updated CHANGELOG.md for v$(VERSION)"

## images: multi-arch build + push by hand; the release workflow does this on a tag
images:
	@test -n "$(VERSION)" || (echo "VERSION is empty (tag the repo or pass VERSION=x.y.z)"; exit 1)
	docker buildx build --platform $(PLATFORMS) \
		--build-arg VERSION=$(VERSION) \
		-t $(DOCKERHUB_REPO):latest \
		-t $(DOCKERHUB_REPO):$(VERSION) \
		-t $(GHCR_REPO):latest \
		-t $(GHCR_REPO):$(VERSION) \
		--push .
	@echo "pushed images $(VERSION)"

## dockerhub-description: push README + recent changelog as the Docker Hub overview
CHANGELOG_VERSIONS ?= 5
dockerhub-description:
	@test -n "$(DOCKERHUB_USER)" || (echo "DOCKERHUB_USER required"; exit 1)
	@test -n "$(DOCKERHUB_TOKEN)" || (echo "DOCKERHUB_TOKEN required"; exit 1)
	doc=$$(mktemp); \
	cat README.md > $$doc; \
	if [ -f CHANGELOG.md ]; then \
		printf '\n\n---\n\n# Changelog\n\n' >> $$doc; \
		awk '/new releases inserted below this line/{f=1;next} f{if(/^## /){n++}; if(n>$(CHANGELOG_VERSIONS)) exit; print}' CHANGELOG.md >> $$doc; \
	fi; \
	token=$$(curl -fsSL -H "Content-Type: application/json" -X POST -d '{"username":"$(DOCKERHUB_USER)","password":"$(DOCKERHUB_TOKEN)"}' https://hub.docker.com/v2/users/login/ | jq -r .token); \
	test -n "$$token" -a "$$token" != "null" || { echo "Docker Hub login failed (check DOCKERHUB_USER/DOCKERHUB_TOKEN)"; rm -f $$doc; exit 1; }; \
	payload=$$(jq -n --rawfile d $$doc '{full_description: $$d}'); \
	body=$$(mktemp); \
	code=$$(curl -sS -o $$body -w '%{http_code}' -X PATCH -H "Authorization: JWT $$token" -H "Content-Type: application/json" -d "$$payload" https://hub.docker.com/v2/repositories/$(DOCKERHUB_REPO)/); \
	rm -f $$doc; \
	case "$$code" in 2*) echo "updated Docker Hub description for $(DOCKERHUB_REPO)"; rm -f $$body;; \
		*) echo "Docker Hub description update failed: HTTP $$code"; cat $$body; echo; rm -f $$body; exit 1;; \
	esac
