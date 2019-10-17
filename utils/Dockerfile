FROM centos:7

COPY nvidia-kubevirt-gpu-device-plugin /usr/bin/nvidia-kubevirt-gpu-device-plugin

COPY pci.ids /usr/pci.ids

CMD ["nvidia-kubevirt-gpu-device-plugin"]

