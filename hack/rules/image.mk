#################
##@ Image
#################

SEMVER = $(shell echo "$(VERSION)" | sed -E 's/^v?([0-9]+\.[0-9]+\.[0-9]+).*/\1/i')

.PHONY: builder
builder: image-prepare stack-image run-image ## Make builder image using pack
	@$(eval BUILDER_IMAGE = $(REGISTRY)/$(REPO)/builder:latest)
	@echo "===> Creating builder image: $(BUILDER_IMAGE)"
	@cd $(IMAGE_DIR) && pack builder create $(BUILDER_IMAGE) --config builder.toml \
	--insecure-registry $(REGISTRY) --publish
	@$(call clean-image,$(BUILDER_IMAGE))

.PHONY: stack-image
stack-image: ## Make stack image
	@$(eval STACK_IMAGE = $(REGISTRY)/$(REPO)/stack:$(SEMVER))
	@$(call build-image,$(VERSION),$(STACK_IMAGE),"stack.Dockerfile",$(IMAGE_DIR))
	@$(call push-image,$(STACK_IMAGE))
	@$(call clean-image,$(STACK_IMAGE))

.PHONY: run-image
run-image: ## Make run image
	@$(eval RUN_IMAGE = $(REGISTRY)/$(REPO)/run:latest)
	@$(call build-image,"latest",$(RUN_IMAGE),"run.Dockerfile",$(IMAGE_DIR))
	@$(call push-image,$(RUN_IMAGE))
	@$(call clean-image,$(RUN_IMAGE))

.PHONY: image-prepare
image-prepare:
	@rm -rf $(IMAGE_DIR) && mkdir -p $(IMAGE_DIR) && cp -rf $(BUILDER_DIR)/* $(IMAGE_DIR)/
	@sed -i 's/{{ \.semver }}/$(SEMVER)/g' $(IMAGE_DIR)/builder.toml
	@sed -i 's/{{ \.semver }}/$(SEMVER)/g' $(IMAGE_DIR)/buildpacks_go/buildpack.toml
	@sed -i "s/\r//g" $(IMAGE_DIR)/buildpacks_go/bin/build
	@sed -i "s/\r//g" $(IMAGE_DIR)/buildpacks_go/bin/detect

######################################################
# build-image
# $(1) VERSION, $(2) IMAGE_NAME, $(3) DOCKERFILE, $(4) BUILD_CONTEXT
define build-image
	@echo "===> Building image: $(2)"
	@echo "     VERSION: $(1)"
	@echo "     DOCKERFILE: $(3)"
	@echo "     CONTEXT: $(4)"
	@cd $(4) && $(RUNTIME) build \
		--build-arg VERSION=$(1) \
		-t $(2) \
		-f $(3) \
		.
	@echo "===> Image built successfully: $(2)"
endef

# push-image
# $(1) IMAGE_NAME
define push-image
	@echo "===> Pushing image: $(1)"
	$(RUNTIME) push $(1)
	@echo "===> Image pushed successfully: $(1)"
endef

# clean-image: 清理本地镜像
# $(1) IMAGE_NAME
define clean-image
	@echo "===> Removing image: $(1)"
	$(RUNTIME) rmi -f $(1)
	@echo "===> Image removed successfully: $(1)"
endef
