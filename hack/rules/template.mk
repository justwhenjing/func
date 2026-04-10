###############
##@ Template
###############

# Go 模板 go.mod 文件路径（支持 2 层深度）
GO_MOD_FILES := $(wildcard \
	$(TEMPLATE_DIR)/go/*/go.mod \
	$(TEMPLATE_DIR)/go/*/*/go.mod \
)

# Rust 模板 Cargo.toml 文件路径
CARGO_FILES := $(wildcard \
	$(TEMPLATE_DIR)/rust/*/Cargo.toml \
	$(TEMPLATE_DIR)/rust/*/*/Cargo.toml \
)

# Node 模板 package-lock.json 文件路径
NODE_LOCK_FILES := $(wildcard \
	$(TEMPLATE_DIR)/node/*/package-lock.json \
	$(TEMPLATE_DIR)/node/*/*/package-lock.json \
)

# TypeScript 模板 package-lock.json 文件路径
TS_LOCK_FILES := $(wildcard \
	$(TEMPLATE_DIR)/typescript/*/package-lock.json \
	$(TEMPLATE_DIR)/typescript/*/*/package-lock.json \
)

# Quarkus mvnw 文件路径
QUARKUS_MVNW_FILES := $(wildcard \
	$(TEMPLATE_DIR)/quarkus/*/mvnw \
	$(TEMPLATE_DIR)/quarkus/*/*/mvnw \
)

# SpringBoot mvnw 文件路径
SPRINGBOOT_MVNW_FILES := $(wildcard \
	$(TEMPLATE_DIR)/springboot/*/mvnw \
	$(TEMPLATE_DIR)/springboot/*/*/mvnw \
)

## 格式化模板
.PHONY: fmt-template
fmt-template: fmt-go ## Format templates

## 不要执行go mod tidy(会导致go.mod被修改)
.PHONY: fmt-go
fmt-go: ## Format Go templates
	@echo "===> run format go templates"
	@for d in $(GO_MOD_FILES); do \
		dir=$$(dirname $$d); \
		echo "format $$dir"; \
		(cd $$dir && go fmt ./...); \
	done

## 校验模板
.PHONY: lint-template
lint-template: lint-go lint-rust lint-typescript ## Run all template lint

.PHONY: lint-go
lint-go: ## Lint Go templates
	@echo "===> run lint go templates"
	@for d in $(GO_MOD_FILES); do \
		(cd $$(dirname $$d) && golangci-lint run); \
	done

.PHONY: lint-rust
lint-rust: ## Lint Rust templates
	@echo "===> run lint rust templates"
	@for d in $(CARGO_FILES); do \
		(cd $$(dirname $$d) && cargo clippy && cargo clean); \
	done

.PHONY: lint-typescript
lint-typescript: ## Lint TypeScript templates
	@echo "===> run lint typescript templates"
	@for d in $(TS_LOCK_FILES); do \
		(cd $$(dirname $$d) && npm_config_cache=/tmp/.npm-cache npm ci && npx eslint --ext .ts . && rm -rf node_modules build); \
	done

## 测试模板
.PHONY: test-template
test-template: test-go test-node test-python test-quarkus test-springboot test-rust test-typescript ## Run all template tests

.PHONY: test-go
test-go: ## Test Go templates
	@echo "===> test go template..."
	@for d in $(GO_MOD_FILES); do \
		(cd $$(dirname $$d) && go test -cover ./...); \
	done

.PHONY: test-node
test-node: ## Test Node templates
	@echo "===> test node template..."
	@for d in $(NODE_LOCK_FILES); do \
		(cd $$(dirname $$d) && npm_config_cache=/tmp/.npm-cache npm ci && npm test && rm -rf node_modules); \
	done

.PHONY: test-python
test-python: ## Test Python templates
	@echo "===> test python template..."
	@./hack/test-python.sh

.PHONY: test-quarkus
test-quarkus: ## Test Quarkus templates
	@echo "===> test quarkus template..."
	@for d in $(QUARKUS_MVNW_FILES); do \
		(cd $$(dirname $$d) && ./mvnw -q test && ./mvnw clean && rm -f .mvn/wrapper/maven-wrapper.jar); \
	done

.PHONY: test-springboot
test-springboot: ## Test Spring Boot templates
	@echo "===> test springboot template..."
	@for d in $(SPRINGBOOT_MVNW_FILES); do \
		(cd $$(dirname $$d) && ./mvnw -q test && ./mvnw clean && rm -f .mvn/wrapper/maven-wrapper.jar); \
	done

.PHONY: test-rust
test-rust: ## Test Rust templates
	@echo "===> test rust template..."
	@for d in $(CARGO_FILES); do \
		(cd $$(dirname $$d) && cargo -q test && cargo clean); \
	done

.PHONY: test-typescript
test-typescript: ## Test Typescript templates
	@echo "===> test typescript template..."
	@for d in $(TS_LOCK_FILES); do \
		(cd $$(dirname $$d) && npm_config_cache=/tmp/.npm-cache npm ci && npm test && rm -rf node_modules build); \
	done

## 清理模板
.PHONY: clean-templates
clean-templates: ## Remove temporary template files
	@echo "===> clean templates..."
	@for d in $(GO_MOD_FILES); do \
		rm -rf $$(dirname $$d)/vendor; \
	done
	@rm -rf $(TEMPLATE_DIR)/go/.static-*/vendor
	@rm -rf $(TEMPLATE_DIR)/go/scaffolding/*/f
	@rm -rf $(TEMPLATE_DIR)/go/scaffolding/*/service
	@rm -rf $(TEMPLATE_DIR)/go/scaffolding/*/vendor
	@rm -rf $(TEMPLATE_DIR)/node/**/node_modules
	@rm -rf $(TEMPLATE_DIR)/python/**/.venv
	@rm -rf $(TEMPLATE_DIR)/python/**/.pytest_cache
	@rm -rf $(TEMPLATE_DIR)/python/**/function/__pycache__
	@rm -rf $(TEMPLATE_DIR)/python/**/tests/__pycache__
	@rm -rf $(TEMPLATE_DIR)/quarkus/**/target
	@rm -rf $(TEMPLATE_DIR)/rust/**/target
	@rm -rf $(TEMPLATE_DIR)/springboot/**/target
	@rm -rf $(TEMPLATE_DIR)/typescript/**/build
	@rm -rf $(TEMPLATE_DIR)/typescript/**/node_modules
