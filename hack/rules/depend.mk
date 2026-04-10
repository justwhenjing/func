#################
##@ Dependencies
#################
GOLANGCI_LINT_VERSION ?= v2.7.0
MOCKGEN_VERSION ?= v0.6.0

.PHONY: depend
depend: golangci-lint mockgen ## Set dependencies

.PHONY: golangci-lint
golangci-lint:
	@echo "===> install golangci-lint..."
	@go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

.PHONY: mockgen
mockgen:
	@echo "===> install mockgen..."
	@go install go.uber.org/mock/mockgen@$(MOCKGEN_VERSION)
