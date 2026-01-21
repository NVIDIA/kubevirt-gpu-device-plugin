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

BUILD_MULTI_ARCH_IMAGES ?= no
DOCKER ?= docker

##### Global variables #####
include $(CURDIR)/versions.mk

ifeq ($(IMAGE_NAME),)
REGISTRY ?= nvidia
IMAGE_NAME := $(REGISTRY)/kubevirt-gpu-device-plugin
endif

GOPROXY ?= https://proxy.golang.org,direct

IMAGE_VERSION := $(VERSION)

IMAGE = $(IMAGE_NAME):$(IMAGE_VERSION)

OUT_IMAGE_NAME ?= $(IMAGE_NAME)
OUT_IMAGE_VERSION ?= $(IMAGE_VERSION)
OUT_IMAGE = $(OUT_IMAGE_NAME):$(OUT_IMAGE_VERSION)

##### Public rules #####
DISTRIBUTIONS := distroless
DEFAULT_PUSH_TARGET := distroless

PUSH_TARGETS := $(patsubst %, push-%, $(DISTRIBUTIONS))
BUILD_TARGETS := $(patsubst %, build-%, $(DISTRIBUTIONS))
TEST_TARGETS := $(patsubst %, build-%, $(DISTRIBUTIONS))

.PHONY: $(DISTRIBUTIONS) $(PUSH_TARGETS) $(BUILD_TARGETS) $(TEST_TARGETS)

ifneq ($(BUILD_MULTI_ARCH_IMAGES),true)
include $(CURDIR)/deployments/container/native-only.mk
else
include $(CURDIR)/deployments/container/multi-arch.mk
endif

# Both distroless and build-distroless trigger a build of the relevant image
$(DISTRIBUTIONS): %: build-%

build-%: DOCKERFILE_SUFFIX = $(*)
build-%: DOCKERFILE = $(CURDIR)/deployments/container/Dockerfile.$(DOCKERFILE_SUFFIX)
$(BUILD_TARGETS): build-%:
	$(DOCKER) build --pull \
		$(DOCKER_BUILD_OPTIONS) \
		$(DOCKER_BUILD_PLATFORM_OPTIONS) \
		--tag $(IMAGE) \
		--build-arg GOLANG_VERSION="$(GOLANG_VERSION)" \
		--build-arg VERSION="$(VERSION)" \
		--build-arg GIT_COMMIT="$(GIT_COMMIT)" \
		--build-arg GOPROXY="$(GOPROXY)" \
		--file $(DOCKERFILE) \
		$(CURDIR)
ifeq ($(PUSH_ON_BUILD),true)
	$(DOCKER) push "$(IMAGE)"
endif

# Handle the default build target.
.PHONY: build
build: $(DEFAULT_PUSH_TARGET)

# Handle the default push target.
.PHONY: push
push: push-$(DEFAULT_PUSH_TARGET)
