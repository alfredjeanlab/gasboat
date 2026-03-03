.PHONY: build build-bridge build-jira-bridge build-gitlab-bridge build-advice-viewer test lint e2e verify image image-agent image-bridge image-jira-bridge image-gitlab-bridge image-advice-viewer image-all push push-agent push-bridge push-jira-bridge push-gitlab-bridge push-advice-viewer push-all helm-package helm-template release release-dry-run clean

VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
REGISTRY ?= ghcr.io/groblegark/gasboat

# ── Controller ──────────────────────────────────────────────────────────

build:
	$(MAKE) -C controller build

build-gb:
	cd controller && go build -ldflags="-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)" -o bin/gb ./cmd/gb/

build-bridge:
	cd controller && go build -ldflags="-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)" -o bin/slack-bridge ./cmd/slack-bridge/

build-jira-bridge:
	cd controller && go build -ldflags="-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)" -o bin/jira-bridge ./cmd/jira-bridge/

build-gitlab-bridge:
	cd controller && go build -ldflags="-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)" -o bin/gitlab-bridge ./cmd/gitlab-bridge/

build-advice-viewer:
	cd controller && go build -ldflags="-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)" -o bin/advice-viewer ./cmd/advice-viewer/

test:
	$(MAKE) -C controller test

lint:
	$(MAKE) -C controller lint

e2e: build-gb
	./tests/e2e/scripts/test-decisions-yield.sh
	./tests/e2e/scripts/test-gate-system.sh

# ── Docker ──────────────────────────────────────────────────────────────

image:
	docker build \
		--platform linux/amd64 \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		-t $(REGISTRY)/controller:$(VERSION) \
		-t $(REGISTRY)/controller:latest \
		controller/

image-agent:
	docker build \
		--platform linux/amd64 \
		-t $(REGISTRY)/agent:$(VERSION) \
		-t $(REGISTRY)/agent:latest \
		-f images/agent/Dockerfile .

image-bridge:
	docker build \
		--platform linux/amd64 \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		-t $(REGISTRY)/slack-bridge:$(VERSION) \
		-t $(REGISTRY)/slack-bridge:latest \
		-f images/slack-bridge/Dockerfile .

image-jira-bridge:
	docker build \
		--platform linux/amd64 \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		-t $(REGISTRY)/jira-bridge:$(VERSION) \
		-t $(REGISTRY)/jira-bridge:latest \
		-f images/jira-bridge/Dockerfile .

image-gitlab-bridge:
	docker build \
		--platform linux/amd64 \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		-t $(REGISTRY)/gitlab-bridge:$(VERSION) \
		-t $(REGISTRY)/gitlab-bridge:latest \
		-f images/gitlab-bridge/Dockerfile .

image-advice-viewer:
	docker build \
		--platform linux/amd64 \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		-t $(REGISTRY)/advice-viewer:$(VERSION) \
		-t $(REGISTRY)/advice-viewer:latest \
		-f images/advice-viewer/Dockerfile .

image-all: image image-agent image-bridge image-jira-bridge image-gitlab-bridge image-advice-viewer

push: image
	docker push $(REGISTRY)/controller:$(VERSION)
	docker push $(REGISTRY)/controller:latest

push-agent: image-agent
	docker push $(REGISTRY)/agent:$(VERSION)
	docker push $(REGISTRY)/agent:latest

push-bridge: image-bridge
	docker push $(REGISTRY)/slack-bridge:$(VERSION)
	docker push $(REGISTRY)/slack-bridge:latest

push-jira-bridge: image-jira-bridge
	docker push $(REGISTRY)/jira-bridge:$(VERSION)
	docker push $(REGISTRY)/jira-bridge:latest

push-gitlab-bridge: image-gitlab-bridge
	docker push $(REGISTRY)/gitlab-bridge:$(VERSION)
	docker push $(REGISTRY)/gitlab-bridge:latest

push-advice-viewer: image-advice-viewer
	docker push $(REGISTRY)/advice-viewer:$(VERSION)
	docker push $(REGISTRY)/advice-viewer:latest

push-all: push push-agent push-bridge push-jira-bridge push-gitlab-bridge push-advice-viewer

# ── Helm ────────────────────────────────────────────────────────────────

helm-template:
	helm template gasboat helm/gasboat/ \
		--set agents.enabled=true \
		--set coopmux.enabled=true \
		--set slackBridge.enabled=true

helm-package:
	helm package helm/gasboat/ --version $(VERSION) --app-version $(VERSION)

# ── Release ────────────────────────────────────────────────────────────

release:
	./scripts/release.sh

release-dry-run:
	./scripts/release.sh --dry-run

verify:
	./scripts/verify-deploy.sh --version=$(VERSION)

# ── Clean ───────────────────────────────────────────────────────────────

clean:
	$(MAKE) -C controller clean
	rm -f gasboat-*.tgz
