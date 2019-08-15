FROM fedora:27

COPY nvidia-vfio-pci-device-plugin /usr/bin/nvidia-vfio-pci-device-plugin

COPY pci.ids /usr/pci.ids

CMD ["nvidia-vfio-pci-device-plugin"]

