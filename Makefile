.PHONY: build build-bridge test lint image image-agent image-bridge image-all push push-agent push-bridge push-all helm-package helm-template clean

VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
REGISTRY ?= ghcr.io/groblegark/gasboat

# ── Controller ──────────────────────────────────────────────────────────

build:
	$(MAKE) -C controller build

build-bridge:
	cd controller && go build -ldflags="-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)" -o bin/slack-bridge ./cmd/slack-bridge/

test:
	$(MAKE) -C controller test

lint:
	$(MAKE) -C controller lint

# ── Docker ──────────────────────────────────────────────────────────────

image:
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		-t $(REGISTRY)/controller:$(VERSION) \
		-t $(REGISTRY)/controller:latest \
		controller/

image-agent:
	docker build \
		-t $(REGISTRY)/agent:$(VERSION) \
		-t $(REGISTRY)/agent:latest \
		images/agent/

image-bridge:
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		-t $(REGISTRY)/slack-bridge:$(VERSION) \
		-t $(REGISTRY)/slack-bridge:latest \
		-f images/slack-bridge/Dockerfile .

image-all: image image-agent image-bridge

push: image
	docker push $(REGISTRY)/controller:$(VERSION)
	docker push $(REGISTRY)/controller:latest

push-agent: image-agent
	docker push $(REGISTRY)/agent:$(VERSION)
	docker push $(REGISTRY)/agent:latest

push-bridge: image-bridge
	docker push $(REGISTRY)/slack-bridge:$(VERSION)
	docker push $(REGISTRY)/slack-bridge:latest

push-all: push push-agent push-bridge

# ── Helm ────────────────────────────────────────────────────────────────

helm-template:
	helm template gasboat helm/gasboat/ \
		--set agents.enabled=true \
		--set coopmux.enabled=true \
		--set slackBridge.enabled=true

helm-package:
	helm package helm/gasboat/ --version $(VERSION) --app-version $(VERSION)

# ── Clean ───────────────────────────────────────────────────────────────

clean:
	$(MAKE) -C controller clean
	rm -f gasboat-*.tgz
