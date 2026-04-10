include hack/rules/depend.mk
include hack/rules/env.mk
include hack/rules/image.mk
include hack/rules/template.mk
include hack/rules/test.mk
include hack/rules/other.mk

# ##
#
# Run 'make help' for a summary
#
# ##
.DEFAULT_GOAL := help

# Help Text
# Headings: lines with `##$` comment prefix
# Targets:  printed if their line includes a `##` comment

.PHONY: help
help:
	@echo 'Usage: make <OPTIONS> ... <TARGETS>'
	@echo ''
	@echo 'Available targets are:'
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z0-9_-]+:.*?##/ \
	{ printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ \
	{ printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)


###############
##@ Development
###############

.PHONY: build
build: $(BIN) ## (default) Build binary for current OS
$(BIN):
	@echo "===> build binary local..."
	@mkdir -p $(BIN_DIR)
	@env CGO_ENABLED=0 GO111MODULE=on go build -trimpath -mod vendor ./cmd/$(BIN) \
	&& mv $(BIN)* $(BIN_DIR)/

.PHONY: test
test: ## Run core unit tests
	@echo "===> run unit test..."
	@mkdir -p $(TEST_DIR)
	@go test -cover -coverprofile=$(TEST_DIR)/cover.out ./...
	@$(MAKE) test-go

.PHONY: lint
lint: ## Run lint test
	@echo "===> run lint..."
	@mkdir -p $(LINT_DIR)
	@golangci-lint run --output.junit-xml.path=$(LINT_DIR)/lint.xml
	@$(MAKE) lint-go

.PHONY: fmt
fmt: ## Run fmt
	@echo "===> run fmt..."
	@go mod tidy && go mod vendor && go fmt ./...
	@$(MAKE) fmt-go

## TODO 本地 generate容易有格式问题
.PHONY: generate
generate: clean-templates ## Run Generate
	@echo "===> run generate..."
	@go generate ./...
	@cp schema/func_yaml-schema.json ${TEMPLATE_DIR}/go/http/
	@cp schema/func_yaml-schema.json ${TEMPLATE_DIR}/go/static-http/
	@go run generate/templates/main.go

.PHONY: clean
clean: clean-templates ## Remove generated artifacts such as binaries and templates
	@echo "===> clean dirs and files"
	@rm -rf $(DIST_DIR)

.PHONY: docs
docs: ## Generating command reference doc
	@echo "===> generate docs..."
	@$(eval TMP_DIR = $(shell mktemp))
	@KUBECONFIG="$(TMP_DIR)" go run docs/generator/main.go && rm -rf $(TMP_DIR)


###############
##@ Version
###############
PLATFORMS ?= linux/amd64 linux/arm64 windows/amd64
RUNTIME ?= docker
REGISTRY ?= zxun-vnfp-release-docker.artnj.zte.com.cn
REPO ?= buildpacks

.PHONY: version
version: clean fmt generate dist ## Make version
	@echo "===> make version..."
	@mkdir -p $(VERSION_DIR)
	@cp $(DOCS_DIR)/use/README.md $(BIN_DIR)/* $(VERSION_DIR)/
	@cd $(VERSION_DIR) && chmod 755 -R * && zip func-$(VERSION).zip *

.PHONY: dist
dist: $(PLATFORMS) ## Make binaries
$(PLATFORMS):
	@$(eval DISTTYPE = $(subst /, ,$@))
	@$(eval BUILD_GOOS = $(word 1, $(DISTTYPE)))
	@$(eval BUILD_ARCH = $(word 2, $(DISTTYPE)))
	@$(eval BIN_EXT = $(if $(filter windows,$(BUILD_GOOS)),.exe,))
	@$(eval BIN_NAME = $(BIN)_$(BUILD_GOOS)_$(BUILD_ARCH)$(BIN_EXT))
	@$(eval ldflags = $(LDFLAGS) \
		-X knative.dev/func/pkg/functions.DefaultBuilderRegistry=$(REGISTRY) \
		-X knative.dev/func/pkg/functions.DefaultBuilderRepo=$(REPO))
	@echo Building for $(BUILD_GOOS) platform, arch is $(BUILD_ARCH)...
	@mkdir -p $(BIN_DIR)
	@env CGO_ENABLED=0 GO111MODULE=on GOOS=$(BUILD_GOOS) GOARCH=$(BUILD_ARCH) \
	go build -o $(BIN_DIR)/$(BIN_NAME) -trimpath -ldflags "$(ldflags) -w -s" ./cmd/$(BIN)
