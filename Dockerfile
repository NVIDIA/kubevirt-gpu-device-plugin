# Copyright (c) 2019-2023, NVIDIA CORPORATION. All rights reserved.
#
# Redistribution and use in source and binary forms, with or without
# modification, are permitted provided that the following conditions
# are met:
#  * Redistributions of source code must retain the above copyright
#    notice, this list of conditions and the following disclaimer.
#  * Redistributions in binary form must reproduce the above copyright
#    notice, this list of conditions and the following disclaimer in the
#    documentation and/or other materials provided with the distribution.
#  * Neither the name of NVIDIA CORPORATION nor the names of its
#    contributors may be used to endorse or promote products derived
#    from this software without specific prior written permission.
#
# THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS ``AS IS'' AND ANY
# EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
# IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR
# PURPOSE ARE DISCLAIMED.  IN NO EVENT SHALL THE COPYRIGHT OWNER OR
# CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL,
# EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO,
# PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR
# PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY
# OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
# (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
# OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

ARG CUDA_IMAGE=cuda
ARG CUDA_VERSION=12.4.1
ARG BASE_DIST=ubi8

FROM nvcr.io/nvidia/${CUDA_IMAGE}:${CUDA_VERSION}-base-${BASE_DIST} as builder

RUN yum install -y wget make gcc

ARG GOLANG_VERSION=1.22.2
RUN wget -nv -O - https://storage.googleapis.com/golang/go${GOLANG_VERSION}.linux-amd64.tar.gz \
    | tar -C /usr/local -xz

ENV GOPATH /go
ENV PATH $GOPATH/bin:/usr/local/go/bin:$PATH

ENV GOOS=linux\
    GOARCH=amd64

WORKDIR /go/src/kubevirt-gpu-device-plugin

COPY . . 

RUN make build

FROM nvcr.io/nvidia/${CUDA_IMAGE}:${CUDA_VERSION}-base-${BASE_DIST}

ARG VERSION

LABEL io.k8s.display-name="NVIDIA KubeVirt GPU Device Plugin"
LABEL name="NVIDIA KubeVirt GPU Device Plugin"
LABEL vendor="NVIDIA"
LABEL version="${VERSION}"
LABEL release="N/A"
LABEL summary="NVIDIA device plugin for KubeVirt"
LABEL description="See summary"

RUN mkdir /licenses && mv /NGC-DL-CONTAINER-LICENSE /licenses/NGC-DL-CONTAINER-LICENSE

COPY --from=builder /go/src/kubevirt-gpu-device-plugin/nvidia-kubevirt-gpu-device-plugin /usr/bin/
COPY --from=builder /go/src/kubevirt-gpu-device-plugin/utils/pci.ids /usr/pci.ids

RUN yum update -y

CMD ["nvidia-kubevirt-gpu-device-plugin"]
