# Copyright (c) NVIDIA CORPORATION.  All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

DOCKER      ?= docker
MKDIR       ?= mkdir
GO          ?= go
PCI_IDS_URL ?= https://pci-ids.ucw.cz/v2.2/pci.ids

include $(CURDIR)/versions.mk

ifeq ($(IMAGE_NAME),)
REGISTRY ?= nvidia
IMAGE_NAME = $(REGISTRY)/kubevirt-gpu-device-plugin
endif

CHECK_TARGETS := lint
MAKE_TARGETS := build check fmt test check-vendor update-pcidb clean $(CHECK_TARGETS)

TARGETS := $(MAKE_TARGETS)

DOCKER_TARGETS := $(patsubst %,docker-%, $(TARGETS))
.PHONY: $(TARGETS) $(DOCKER_TARGETS)

GOOS ?= linux

build:
	GOOS=$(GOOS) go build -trimpath -o nvidia-kubevirt-gpu-device-plugin ./cmd

all: check test build
check: $(CHECK_TARGETS)

# Apply go fmt to the codebase
fmt:
	go list -f '{{.Dir}}' $(MODULE)/... \
		| xargs gofmt -s -l -w

goimports:
	go list -f {{.Dir}} $(MODULE)/... \
		| xargs goimports -local $(MODULE) -w

lint:
	golangci-lint run ./...

COVERAGE_FILE := coverage.out
test: build
	go test -coverprofile=$(COVERAGE_FILE).with-mocks $(MODULE)/...

coverage: test
	cat $(COVERAGE_FILE).with-mocks | grep -v "_mock.go" > $(COVERAGE_FILE)
	go tool cover -func=$(COVERAGE_FILE)

vendor:  | mod-tidy mod-vendor mod-verify

mod-tidy:
	@for mod in $$(find . -name go.mod -not -path "./testdata/*" -not -path "./third_party/*"); do \
	    echo "Tidying $$mod..."; ( \
	        cd $$(dirname $$mod) && go mod tidy \
            ) || exit 1; \
	done

mod-vendor:
	@for mod in $$(find . -name go.mod -not -path "./testdata/*" -not -path "./third_party/*" -not -path "./deployments/*"); do \
		echo "Vendoring $$mod..."; ( \
			cd $$(dirname $$mod) && go mod vendor \
			) || exit 1; \
	done

mod-verify:
	@for mod in $$(find . -name go.mod -not -path "./testdata/*" -not -path "./third_party/*"); do \
	    echo "Verifying $$mod..."; ( \
	        cd $$(dirname $$mod) && go mod verify | sed 's/^/  /g' \
	    ) || exit 1; \
	done

check-vendor: vendor
	git diff --exit-code HEAD -- go.mod go.sum vendor

update-pcidb:
	wget $(PCI_IDS_URL) -O $(CURDIR)/utils/pci.ids

clean:
	rm -rf nvidia-kubevirt-gpu-device-plugin && rm -rf coverage.out
