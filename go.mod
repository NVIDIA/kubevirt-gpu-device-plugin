module kubevirt-gpu-device-plugin

go 1.12

require (
	github.com/NVIDIA/gpu-monitoring-tools v0.0.0-20211102125545-5a2c58442e48
	github.com/fsnotify/fsnotify v1.4.9
	github.com/golang/glog v1.0.0
	github.com/onsi/ginkgo v1.11.0
	github.com/onsi/gomega v1.7.0
	google.golang.org/grpc v1.53.0
	k8s.io/kubelet v0.19.16
)

replace (
	github.com/gogo/protobuf => github.com/gogo/protobuf v1.3.2
	google.golang.org/protobuf => google.golang.org/protobuf v1.28.0
)
