module kubevirt-gpu-device-plugin

go 1.18

require (
	github.com/NVIDIA/gpu-monitoring-tools v0.0.0-20211102125545-5a2c58442e48
	github.com/fsnotify/fsnotify v1.4.9
	github.com/onsi/ginkgo v1.11.0
	github.com/onsi/gomega v1.7.0
	google.golang.org/grpc v1.56.2
	k8s.io/klog/v2 v2.100.1
	k8s.io/kubelet v0.19.16
)

require (
	github.com/go-logr/logr v1.2.4 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/protobuf v1.5.3 // indirect
	github.com/hpcloud/tail v1.0.0 // indirect
	golang.org/x/net v0.12.0 // indirect
	golang.org/x/sys v0.10.0 // indirect
	golang.org/x/text v0.11.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20230710151506-e685fd7b542b // indirect
	google.golang.org/protobuf v1.31.0 // indirect
	gopkg.in/fsnotify.v1 v1.4.7 // indirect
	gopkg.in/tomb.v1 v1.0.0-20141024135613-dd632973f1e7 // indirect
	gopkg.in/yaml.v2 v2.2.8 // indirect
)

replace (
	github.com/gogo/protobuf => github.com/gogo/protobuf v1.3.2
	google.golang.org/protobuf => google.golang.org/protobuf v1.28.0
)
