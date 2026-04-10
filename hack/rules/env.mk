# Dirs
WORK_DIR          := $(shell pwd)
DIST_DIR          := $(WORK_DIR)/dist
TEMPLATE_DIR      := $(WORK_DIR)/templates
BUILDER_DIR       := $(WORK_DIR)/builder
DOCS_DIR          := $(WORK_DIR)/docs
BIN_DIR           := $(DIST_DIR)/bin
TEST_DIR          := $(DIST_DIR)/test
LINT_DIR          := $(DIST_DIR)/lint
E2E_DIR           := $(DIST_DIR)/e2e
VERSION_DIR       := $(DIST_DIR)/version
IMAGE_DIR         := $(DIST_DIR)/image

# Binaries
BIN               := func

# Version
HASH         := $(shell git rev-parse --short HEAD 2>/dev/null)
VERSION      ?= $(shell git describe --tags --match 'v*' 2>/dev/null || echo "V7.26.10.06")
VTAG         := $(shell git tag --points-at HEAD | head -1)
KVER         ?= $(shell git describe --tags --match 'knative-*' 2>/dev/null || echo "knative-v0.0.1")
LDFLAGS      := -X knative.dev/func/pkg/version.Vers=$(VERSION) 
LDFLAGS += -X knative.dev/func/pkg/version.Kver=$(KVER) 
LDFLAGS += -X knative.dev/func/pkg/version.Hash=$(HASH)

FUNC_UTILS_IMG ?= ghcr.io/knative/func-utils:v2
LDFLAGS += -X knative.dev/func/pkg/k8s.SocatImage=$(FUNC_UTILS_IMG)
LDFLAGS += -X knative.dev/func/pkg/k8s.TarImage=$(FUNC_UTILS_IMG)
LDFLAGS += -X knative.dev/func/pkg/pipelines/tekton.FuncUtilImage=$(FUNC_UTILS_IMG)

GOFLAGS      := "-ldflags=$(LDFLAGS)"
export GOFLAGS

# Makefile env
MAKEFILE_DIR := $(dir $(abspath $(lastword $(MAKEFILE_LIST))))
MAKEFLAGS += --output-sync=none
