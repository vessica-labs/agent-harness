.DEFAULT_GOAL := help

VERSION ?=
BUILD_VERSION := $(if $(strip $(VERSION)),$(strip $(VERSION)),$(shell git describe --tags --always --dirty 2>/dev/null || printf 'dev'))
RELEASE_VERSION_ARG := $(if $(strip $(VERSION)),--version "$(strip $(VERSION))",)
PREFIX ?= $(HOME)/.local
RAILWAY_PROJECT ?= 311e0195-76c3-4840-a7ed-7e4083b49f51
RAILWAY_ENVIRONMENT ?= production
RAILWAY_SERVICE ?= control-plane
PUBLIC_URL ?= https://control-plane-production-12ec.up.railway.app
RELEASE := python3 scripts/release.py
RELEASE_ARGS := --project $(RAILWAY_PROJECT) --environment $(RAILWAY_ENVIRONMENT) --service $(RAILWAY_SERVICE) --url $(PUBLIC_URL)

.PHONY: help build install test verify release-check publish checkpoint deploy-production release production-status

help:
	@printf '%s\n' \
		'make build                         Build the CLI from the current checkout' \
		'make install                       Install the current checkout to PREFIX/bin' \
		'make verify                        Run every local release check' \
		'make release-check                 Validate the automatically selected next RC' \
		'make publish [VERSION=vX.Y.Z]      Push main and the tag; wait for GitHub artifacts' \
		'make checkpoint VERSION=vX.Y.Z     Create the matching Railway worker checkpoint' \
		'make deploy-production VERSION=vX.Y.Z  Point production at the tagged image and verify it' \
		'make release [VERSION=vX.Y.Z]      Publish the next RC, deploy, and verify production' \
		'make production-status             Show the deployed image and check health endpoints' \
		'' \
		'Overrides: PREFIX, RAILWAY_PROJECT, RAILWAY_ENVIRONMENT, RAILWAY_SERVICE, PUBLIC_URL'

build:
	$(MAKE) -C cloud-runner build VERSION="$(BUILD_VERSION)"

install:
	$(MAKE) -C cloud-runner install VERSION="$(BUILD_VERSION)" PREFIX="$(PREFIX)"

test:
	python3 -m unittest discover -s tests -v
	$(MAKE) -C cloud-runner test

verify:
	python3 -m unittest discover -s tests -v
	$(MAKE) -C cloud-runner verify VERSION="$(BUILD_VERSION)"

release-check:
	$(RELEASE) check $(RELEASE_VERSION_ARG) $(RELEASE_ARGS)

publish:
	$(RELEASE) publish $(RELEASE_VERSION_ARG) $(RELEASE_ARGS)

checkpoint:
	@test -n "$(strip $(VERSION))" || { printf '%s\n' 'VERSION is required when resuming at checkpoint'; exit 2; }
	$(RELEASE) checkpoint --version "$(VERSION)" $(RELEASE_ARGS)

deploy-production:
	@test -n "$(strip $(VERSION))" || { printf '%s\n' 'VERSION is required when resuming at deploy-production'; exit 2; }
	$(RELEASE) deploy --version "$(VERSION)" $(RELEASE_ARGS)

release:
	$(RELEASE) release $(RELEASE_VERSION_ARG) $(RELEASE_ARGS)

production-status:
	$(RELEASE) status $(RELEASE_ARGS)
