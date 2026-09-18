# NVIDIA K8s Device Plugin to assign GPUs and vGPUs to KubeVirt VMs

> Starting from v1.1.0, we will only be supporting KubeVirt v0.36.0 or newer. Please use v1.0.1 for compatibility with older KubeVirt versions.

## Table of Contents
- [About](#about)
- [Features](#features)
- [Prerequisites](#prerequisites)
- [Quick Start](#quick-start)
- [Docs](#docs)

## About
This is a kubernetes device plugin that can discover and expose GPUs and vGPUs on a kubernetes node. This device plugin will enable launching GPU attached [KubeVirt](https://github.com/kubevirt/kubevirt/blob/master/README.md) VMs in your kubernetes cluster. Its specifically developed to serve KubeVirt workloads in a Kubernetes cluster.


## Features
- Discovers Nvidia GPUs which are bound to VFIO-PCI driver and exposes them as devices available to be attached to VM in passthrough mode.
- Discovers Nvidia vGPUs configured on a kubernetes node and exposes them to be attached to KubeVirt VMs
- Performs basic health checks on the GPU on a kubernetes node.

## Prerequisites
- Need to have Nvidia GPU configured for GPU passthrough or vGPU. Quickstart section provides details about this
- Kubernetes version >= v1.11
- KubeVirt release >= v0.36.0
- KubeVirt GPU feature gate should be enabled and permitted devices should be whitelisted. Feature gate is enabled by creating a ConfigMap. ConfigMap yaml can be found under `/examples`.

## Quick Start

Before starting the device plug, the GPUs on a kubernetes node need to be configured to be in GPU passthrough mode or vGPU mode

### Whitelist GPU and vGPU in KubeVirt CR
GPUs and vGPUs should be allowlisted in the KubeVirt CR following the instructions outlined [here](https://kubevirt.io/user-guide/virtual_machines/host-devices/#listing-permitted-devices). An example KubeVirt CR can be found under `/examples`.

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
###### Legacy Mode (BIOS)
```shell 
grub2-mkconfig -o /boot/grub2/grub.cfg
reboot
```
###### UEFI Mode
```shell 
grub2-mkconfig -o /boot/efi/EFI/centos/grub.cfg
reboot
```

After rebooting, verify IOMMU is enabled using the following command
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
**Update config to load VFIO-PCI module after reboot**
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
##### 5. Write the UUID that you obtained in the previous step to create the file in the registration information directory for the vGPU type that you want to create.
```shell
$ echo "uuid"> subdirectory/create
```
**uuid** -- The UUID that you generated in the previous step, which will become the UUID of the vGPU that you want to create.

**subdirectory** -- The registration information directory for the vGPU type that you want to create, for example, nvidia-41.

This example creates an instance of the M10-2Q vGPU type with the UUID aa618089-8b16-4d01-a136-25a0f3c73123.
```shell
$ echo "aa618089-8b16-4d01-a136-25a0f3c73123" > nvidia-41/create
```
An mdev device file for the vGPU is added to the parent physical device directory of the vGPU. The vGPU is identified by its UUID.

The /sys/bus/mdev/devices/ directory contains a symbolic link to the mdev device file.

##### 6. Confirm that the vGPU was created.
```shell
$ ls -l /sys/bus/mdev/devices/
total 0
lrwxrwxrwx. 1 root root 0 Nov 24 13:33 aa618089-8b16-4d01-a136-25a0f3c73123 -> ../../../devices/pci0000:00/0000:00:03.0/0000:03:00.0/0000:04:09.0/0000:06:00.0/aa618089-8b16-4d01-a136-25a0f3c73123
```

--------------------------------------------------------------
### Preparing a GPU to be used in vGPU mode (Ada Lovelace / Hopper and newer)

Starting with vGPU release 17, GPUs based on Ada Lovelace and newer architectures (for example L40S, H100, H200) no longer expose vGPU instances through mdev. `/sys/bus/mdev` does not exist on these hosts. Instead, vGPU profiles are assigned directly on each SR-IOV Virtual Function through a vendor-specific VFIO sysfs interface, and this plugin discovers vGPUs on such hosts through that interface instead of mdev. The steps below replace the mdev steps above; do not use both on the same GPU.

##### 1. Enable SR-IOV Virtual Functions on the physical GPU.
```shell
$ /usr/lib/nvidia/sriov-manage -e 0000:41:00.0
```
**0000:41:00.0** -- The PCI BDF of the physical GPU (Physical Function).

##### 2. Find the vGPU types that can be created on a Virtual Function.
```shell
$ cat /sys/bus/pci/devices/0000\:41\:00.4/nvidia/creatable_vgpu_types
1428 : NVIDIA H200X-141C
1414 : NVIDIA H200X-1-18C
```
Each line lists a numeric type id and the corresponding profile name.

##### 3. Create a vGPU on the Virtual Function by writing its type id.
```shell
$ echo 1414 > /sys/bus/pci/devices/0000\:41\:00.4/nvidia/current_vgpu_type
```

##### 4. Confirm the vGPU was created.
```shell
$ cat /sys/bus/pci/devices/0000\:41\:00.4/nvidia/current_vgpu_type
1414
```
A non-zero value confirms the Virtual Function now has a vGPU profile configured; this plugin advertises it as a `nvidia.com/<profile-name>` resource, grouped separately from Virtual Functions configured with a different profile.

## Docs
### Deployment
The Daemonset creation yaml can be used to deploy the device plugin. 
```
kubectl apply -f nvidia-kubevirt-gpu-device-plugin.yaml
```

Example YAML files for creating VMs with GPU/vGPU are in the `examples` folder

#### vGPU profile timing (vendor-specific VFIO)

By default the plugin scans the vendor-specific VFIO vGPU Virtual Functions once at startup and does not re-scan while running. Every profile a node should advertise must therefore be configured on its Virtual Functions (step 3 of "Preparing a GPU to be used in vGPU mode (Ada Lovelace / Hopper and newer)") **before** the device plugin pod starts. A Virtual Function whose profile is created or changed after startup is not picked up until the pod restarts. After a node reboot, order the plugin after whatever recreates the Virtual Functions (for example an init container that waits until the count of configured Virtual Functions is non-zero and stable) so it discovers the full set.

Optionally, set the `VFIO_VGPU_RESCAN_INTERVAL` env var to a Go duration (for example `30s`) to enable periodic rediscovery. The plugin then re-runs the same scan on that interval and, while running, starts advertising a newly configured profile, updates the device count of a profile whose set of Virtual Functions changed, and stops advertising a profile once none of its Virtual Functions remain configured. Rediscovery is disabled when the variable is unset, empty or non-positive, so the default behavior above is unchanged; a positive value below five seconds is clamped up to five seconds. A periodic rescan is used rather than an inotify watch because inotify does not fire reliably for the sysfs attribute writes that (re)configure a vGPU, and SR-IOV Virtual Function creation/removal moves whole device directories that are awkward to watch correctly. On a fully consumed card each rescan may consult NVML (see the NVML fallback section below), so choose an interval no shorter than you need.

#### NVML fallback (fully consumed nodes)

The profile name of a configured Virtual Function is normally read from its card's `creatable_vgpu_types` catalog. On a fully consumed card that catalog is reduced to its header on every function, so the plugin resolves the configured type ids through NVML instead (`GetSupportedVgpus`, whose list does not shrink as capacity is allocated). This path is reached only on fully consumed nodes; the default manifest does not enable it.

NVML needs two things inside the container:

- **`libnvidia-ml.so.1`** from the host driver, loadable by the dynamic linker. Bind the **single file**, not the host library directory: putting the host lib directory on `LD_LIBRARY_PATH` drags the host glibc into the container and breaks the dynamic linker. This single-file requirement was confirmed on hardware.
- **The NVIDIA device nodes** the queries touch: `/dev/nvidiactl` (the NVML control device) and the per-GPU `/dev/nvidiaN` for each physical card. The management-only queries this plugin makes (`DeviceGetHandleByPciBusId`, `GetSupportedVgpus`, `GetName`) do not open a CUDA context or MIG capabilities, so `/dev/nvidia-uvm`, `/dev/nvidia-uvm-tools` and `/dev/nvidia-caps` are not needed. `/dev/nvidiactl` plus the per-GPU nodes were confirmed on hardware; the narrower "uvm/caps not required" scope is inferred from the set of NVML calls above, not separately tested.

There are two supported ways to provide these:

1. **NVIDIA Container Toolkit (`runtimeClassName: nvidia`) — minimally privileged, recommended.** When the toolkit is installed on the node, set `runtimeClassName: nvidia` and the container env `NVIDIA_VISIBLE_DEVICES=all` and `NVIDIA_DRIVER_CAPABILITIES=utility` (the `utility` capability is the one that provides NVML). The toolkit's runtime hook then injects `libnvidia-ml.so.1` and the device nodes and adds the matching device-cgroup rules, so the container stays non-privileged. This is the standard toolkit mechanism; the exact `runtimeClassName` form was not part of this feature's hardware validation.
2. **hostPath — privileged.** `manifests/nvidia-kubevirt-gpu-device-plugin-nvml.yaml` single-file-binds `libnvidia-ml.so.1`, sets `LD_LIBRARY_PATH`, mounts only `/dev/nvidiactl` and the per-GPU `/dev/nvidiaN` nodes (not the whole host `/dev`) and runs `privileged: true`. A hostPath device node is not added to the container's device cgroup, so a non-privileged container is denied when it opens the node regardless of the mount, and a plain pod spec has no field to grant a single host device node; `privileged: true` is the only pod-spec-native way to make host device nodes usable without the toolkit. The hardware validation of the fallback used this hostPath + privileged mechanism with the whole host `/dev` mounted; this manifest narrows that to `/dev/nvidiactl` plus the per-GPU nodes, which are the only device nodes the NVML calls above use. Point the `libnvidia-ml.so.1` hostPath at wherever the host driver installed it (find it with `ldconfig -p | grep libnvidia-ml`).

### Build

Build executable binary using make:
```shell
make
```
To build a container image, first export the following variables:
e.g.
```shell
export IMAGE_NAME="quay.io/nvidia/kubevirt-gpu-device-plugin"
export VERSION=devel
```
[Optional] To build a multi-arch container image, export the following variable:
```shell
export BUILD_MULTI_ARCH_IMAGES=true
```
Build container image:
```shell
make -f deployments/container/Makefile build
```
Push container image to container registry:
```shell
make -f deployments/container/Makefile push
```
### To Do
- Improve the healthcheck mechanism for GPUs with VFIO-PCI drivers
- Support GetPreferredAllocation API of DevicePluginServer. It returns a preferred set of devices to allocate from a list of available ones. The resulting preferred allocation is not guaranteed to be the allocation ultimately performed by the devicemanager. It is only designed to help the devicemanager make a more informed allocation decision when possible. It has not been implemented in kubevirt-gpu-device-plugin.
--------------------------------------------------------------
