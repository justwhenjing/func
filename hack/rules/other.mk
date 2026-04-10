###############
##@ Other
###############

## 证书相关(暂时用不到)
.PHONY: certs
certs: templates/certs/ca-certificates.crt ## Update root certificates

.PHONY: templates/certs/ca-certificates.crt
templates/certs/ca-certificates.crt: ## Updating root certificates
	curl --output templates/certs/ca-certificates.crt https://curl.se/ca/cacert.pem

# hack相关(暂时用不到)
.PHONY: generate-hack-components
generate-hack-components: ## Generate Hack Components
	@cd hack && go run ./cmd/components

.PHONY: test-hack
test-hack: ## Test hack
	@echo "===> test hack..."
	@cd hack && go test ./... -v
