###################
##@ Extended Testing (cluster required)
###################

# See target "test" for unit tests only

.PHONY: test-integration
test-integration: ## Run integration tests using an available cluster.
	@echo "===> test integration..."
	@mkdir -p $(E2E_DIR)
	@go test -cover -coverprofile=$(E2E_DIR)/cover.out -tags integration -timeout 60m ./... -v -run TestInt_

# Runtime and other options can be configured using the FUNC_E2E_* environment variables. see e2e_test.go
.PHONY: test-e2e
test-e2e: func-instrumented-bin ## Basic E2E tests (includes core, metadata and remote tests)
	@echo "===> test e2e..."
	@mkdir -p $(E2E_DIR)
	@FUNC_E2E_BIN=$(BIN_DIR)/$(BIN) go test -tags e2e -timeout 60m ./e2e -v -run "TestCore_|TestMetadata_|TestRemote_"
	@go tool covdata textfmt -i=$${FUNC_E2E_GOCOVERDIR:-.coverage} -o $(E2E_DIR)/cover.out

# see e2e_test.go for available options
.PHONY: test-e2e-podman
test-e2e-podman: func-instrumented-bin ## Run E2E Podman-specific tests
	@echo "===> test e2e podman..."
	@mkdir -p $(E2E_DIR)
	@FUNC_E2E_BIN=$(BIN_DIR)/$(BIN) FUNC_E2E_PODMAN=true go test -tags e2e -timeout 60m ./e2e -v -run TestPodman_
	@go tool covdata textfmt -i=$${FUNC_E2E_GOCOVERDIR:-.coverage} -o $(E2E_DIR)/cover.out

# Runtime and other options can be configured using the FUNC_E2E_* environment variables. see e2e_test.go
.PHONY: test-e2e-matrix
test-e2e-matrix: func-instrumented-bin ## Basic E2E tests (includes core, metadata and remote tests)
	@echo "===> test e2e metrix..."
	@mkdir -p $(E2E_DIR)
	@FUNC_E2E_BIN=$(BIN_DIR)/$(BIN) FUNC_E2E_MATRIX=true go test -tags e2e -timeout 120m ./e2e -v -run TestMatrix_
	@go tool covdata textfmt -i=$${FUNC_E2E_GOCOVERDIR:-.coverage} -o $(E2E_DIR)/cover.out

.PHONY: test-full
test-full: func-instrumented-bin ## Run full test suite with all checks enabled
	./hack/test-full.sh

.PHONY: test-full-logged
test-full-logged: func-instrumented-bin ## Run full test and log with timestamps (requires python)
	./hack/test-full.sh 2>&1 | python -u -c "import sys; from datetime import datetime; [print(f'[{datetime.now().strftime(\"%H:%M:%S\")}] {line}', end='', flush=True) for line in sys.stdin]" | tee ./test-full.log
	@echo '🎉 Full Test Complete.  Log stored in test-full.log'

.PHONY: func-instrumented-bin
func-instrumented-bin: ## func binary instrumented with coverage reporting
	@env CGO_ENABLED=1 go build -cover -o $(BIN_DIR)/$(BIN) ./cmd/$(BIN)
