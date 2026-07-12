SHELL := /usr/bin/env bash

GO ?= go
GOFMT ?= gofmt
HELM ?= helm
KUBECTL ?= kubectl
K3S ?= /usr/local/bin/k3s
JQ ?= jq
REGISTRY ?= localhost
TAG ?= dev
SERVICES := control-plane \
	openai-gateway openai-responses-gateway anthropic-gateway gemini-gateway \
	openai-chat-adapter openai-responses-adapter anthropic-adapter gemini-adapter
IMAGE_PREFIX := $(if $(REGISTRY),$(REGISTRY)/,)
IMAGES := $(foreach service,$(SERVICES),$(IMAGE_PREFIX)gwai-$(service):$(TAG))

.PHONY: all build test test-race vet fmt-check contracts-check scripts-check check images images-load-k3s local-deploy e2e-k3s helm-lint deploy undeploy port-forward

all: check build

build:
	@mkdir -p bin
	@for service in $(SERVICES); do \
		$(GO) build -trimpath -o bin/$$service ./cmd/$$service; \
	done

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

fmt-check:
	@test -z "$$($(GOFMT) -l .)" || { $(GOFMT) -l .; echo "Go files are not formatted"; exit 1; }

contracts-check:
	$(JQ) empty api/ir/*.json

scripts-check:
	bash -n scripts/*.sh

check: fmt-check vet test-race contracts-check scripts-check helm-lint

images:
	@for service in $(SERVICES); do \
		docker build --build-arg SERVICE=$$service \
			-t $(IMAGE_PREFIX)gwai-$$service:$(TAG) .; \
	done

images-load-k3s:
	@mkdir -p dist
	@set -euo pipefail; for service in $(SERVICES); do \
		rm -f dist/gwai-$$service.tar; \
		docker save -o dist/gwai-$$service.tar $(IMAGE_PREFIX)gwai-$$service:$(TAG); \
		sudo $(K3S) ctr images import dist/gwai-$$service.tar; \
	done

local-deploy: images images-load-k3s deploy
	$(KUBECTL) --namespace gwai rollout restart deployment \
		--selector app.kubernetes.io/instance=gwai
	$(KUBECTL) --namespace gwai rollout status deployment \
		--selector app.kubernetes.io/instance=gwai --timeout=180s

e2e-k3s:
	GO=$(GO) ./scripts/e2e-k3s.sh

helm-lint:
	$(HELM) lint deploy/helm/gwai
	$(HELM) lint deploy/helm/gwai -f deploy/helm/gwai/ci/multi-provider-values.yaml

deploy:
	$(HELM) upgrade --install gwai deploy/helm/gwai \
		--namespace gwai --create-namespace \
		--set-string global.image.registry=$(REGISTRY) \
		--set global.image.tag=$(TAG)

undeploy:
	$(HELM) uninstall gwai --namespace gwai

port-forward:
	$(KUBECTL) --namespace gwai port-forward service/gwai-openai-gateway 8080:8080
