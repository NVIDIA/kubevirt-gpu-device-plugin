# NVIDIA K8s Device Plugin to assign GPUs and vGPUs to Kubevirt VMs

## Table of Contents
- [About](#about)
- [Features](#features)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Docs](#docs)

## About
This is a kubernetes device plugin that can discover and expose GPUs and vGPUs on a kubernetes node. This device plugin will enable to launch GPU attached [Kubevirt](https://github.com/kubevirt/kubevirt/blob/master/README.md) VMs in your kubernetes cluster. Its specifically developed to serve Kubevirt workloads in a Kubernetes cluster.


## Features
- Discovers Nvidia GPUs which are bound to VFIO-PCI driver and exposes them as devices available to be attached to VM in pass through mode.
- Discovers Nvidia vGPUs configured on a kubernetes node and exposes them to be attached to Kubevirt VMs
- Performs basic health check on the GPU on a kubernetes node.

## Prerequisites
- Need to have Nvidia GPU configured for GPU pass thorugh or vGPU. Quickstart section provides details about this
- Kubernetes version >= v1.11
- Kubevirt release >= v0.22.0
- Kubevirt GPU feature gate should be enabled. Feature gate is enabled by creating a ConfigMap. ConfigMap yaml can be found under /examples

## Quick Start

Before starting the device plug, the GPUs on a kubernetes node need to configured to be in GPU pass through mode or vGPU mode

### Preparing a GPU to be used in pass through mode
GPU needs to be loaded with VFIO-PCI driver to be used in pass through mode

##### 1. Enable IOMMU and blacklist nouveau driver on KVM Host

  Append "**intel_iommu=on modprobe.blacklist=nouveau**" to "**GRUB_CMDLINE_LINUX**" 
```shell
$ vi /etc/default/grub
# line 6: add (if AMD CPU, add [amd_iommu=on])
GRUB_TIMEOUT=5
GRUB_DISTRIBUTOR="$(sed 's, release .*$,,g' /etc/system-release)"
GRUB_DEFAULT=saved
GRUB_DISABLE_SUBMENU=true
GRUB_TERMINAL_OUTPUT="console"
GRUB_CMDLINE_LINUX="rd.lvm.lv=centos/root rd.lvm.lv=centos/swap rhgb quiet intel_iommu=on modprobe.blacklist=nouveau"
GRUB_DISABLE_RECOVERY="true"
```
```shell
grub2-mkconfig -o /boot/grub2/grub.cfg
reboot
```
After rebooting, verify IOMMU is enabled using following command
```shell
dmesg | grep -E "DMAR|IOMMU"
```
Verify that nouveau is disabled
```shell
dmesg | grep -i nouveau
```

##### 2. Enable vfio-pci kernel module

**Determine vendor-ID and device-ID of the GPU using following command**

```shell
lspci -nn | grep -i nvidia
```
In the example below the vendor-ID is 10de and device-ID is 1b38
```shell
$ lspci -nn | grep -i nvidia
04:00.0 3D controller [0302]: NVIDIA Corporation GP102GL [Tesla P40] [10de:1b38] (rev a1)
```

**Update VFIO config**
```shell
echo "options vfio-pci ids=vendor-ID:device-ID" > /etc/modprobe.d/vfio.conf
```
Considering vendor-ID is 10de and device-ID is 1b38 command will be as follows
```shell
echo "options vfio-pci ids=10de:1b38" > /etc/modprobe.d/vfio.conf
```
**Update config to load VFIO-PCI module after reboo**t
```shell
echo 'vfio-pci' > /etc/modules-load.d/vfio-pci.conf
reboot
```

**Verify VFIO-PCI driver is loaded for the GPU**
```shell
lspci -nnk -d 10de:
```
Output below shows that "Kernel driver in use" is "vfio-pci"
```shell
$ lspci -nnk -d 10de:
04:00.0 3D controller [0302]: NVIDIA Corporation GP102GL [Tesla P40] [10de:1b38] (rev a1)
        Subsystem: NVIDIA Corporation Device [10de:11d9]
        Kernel driver in use: vfio-pci
        Kernel modules: nouveau
```
--------------------------------------------------------------
### Preparing a GPU to be used in vGPU mode
Nvidia Virtual GPU manager needs to be installed on the host to configure GPUs in vGPU mode.

##### 1. Change to the mdev_supported_types directory for the physical GPU.
```shell
$ cd /sys/class/mdev_bus/domain\:bus\:slot.function/mdev_supported_types/
```
This example changes to the mdev_supported_types directory for the GPU with the domain 0000 and PCI device BDF 06:00.0.
```shell
$ cd /sys/bus/pci/devices/0000\:06\:00.0/mdev_supported_types/
```
##### 2. Find out which subdirectory of mdev_supported_types contains registration information for the vGPU type that you want to create.
```shell
$ grep -l "vgpu-type" nvidia-*/name
vgpu-type
```
The vGPU type, for example, M10-2Q.
This example shows that the registration information for the M10-2Q vGPU type is contained in the nvidia-41 subdirectory of mdev_supported_types.
```shell
$ grep -l "M10-2Q" nvidia-*/name
nvidia-41/name
```
##### 3. Confirm that you can create an instance of the vGPU type on the physical GPU.
```shell
$ cat subdirectory/available_instances
```
**subdirectory** -- The subdirectory that you found in the previous step, for example, nvidia-41.

The number of available instances must be at least 1. If the number is 0, either an instance of another vGPU type already exists on the physical GPU, or the maximum number of allowed instances has already been created.

This example shows that four more instances of the M10-2Q vGPU type can be created on the physical GPU.
```shell
$ cat nvidia-41/available_instances
4
```
##### 4. Generate a correctly formatted universally unique identifier (UUID) for the vGPU.
```shell
$ uuidgen
aa618089-8b16-4d01-a136-25a0f3c73123
```
##### 5. Write the UUID that you obtained in the previous step to the create file in the registration information directory for the vGPU type that you want to create.
```shell
$ echo "uuid"> subdirectory/create
```
**uuid** -- The UUID that you generated in the previous step, which will become the UUID of the vGPU that you want to create.

**subdirectory** -- The registration information directory for the vGPU type that you want to create, for example, nvidia-41.

This example creates an instance of the M10-2Q vGPU type with the UUID aa618089-8b16-4d01-a136-25a0f3c73123.
```shell
$ echo "aa618089-8b16-4d01-a136-25a0f3c73123" > nvidia-41/create
```
An mdev device file for the vGPU is added is added to the parent physical device directory of the vGPU. The vGPU is identified by its UUID.

The /sys/bus/mdev/devices/ directory contains a symbolic link to the mdev device file.

##### 6. Confirm that the vGPU was created.
```shell
$ ls -l /sys/bus/mdev/devices/
total 0
lrwxrwxrwx. 1 root root 0 Nov 24 13:33 aa618089-8b16-4d01-a136-25a0f3c73123 -> ../../../devices/pci0000:00/0000:00:03.0/0000:03:00.0/0000:04:09.0/0000:06:00.0/aa618089-8b16-4d01-a136-25a0f3c73123
```

## Docs
### Deployment
The daemon set creation yaml can be used to deploy the device plugin. 
```
kubectl apply -f nvidia-kubevirt-gpu-device-plugin.yaml
```

Examples yamls for creating VMs with GPU/vGPU are in the `examples` folder

### Build
Build executable binary using make
```shell
make
```
Build docker image
```shell
make build-image DOCKER_REPO=<docker-repo-url> DOCKER_TAG=<image-tag>
```
Push docker image to a docker repo
```shell
make push-image DOCKER_REPO=<docker-repo-url> DOCKER_TAG=<image-tag>
```
### To Do
- Improve the healthcheck mechanism for GPUs with VFIO-PCI drivers
--------------------------------------------------------------
