module kubevirt-gpu-device-plugin

go 1.12

require (
	github.com/NVIDIA/gpu-monitoring-tools v0.0.0-20200116003318-021662a21098
	github.com/fsnotify/fsnotify v1.4.7
	github.com/gogo/protobuf v0.0.0-20170330071051-c0656edd0d9e // indirect
	github.com/golang/glog v0.0.0-20160126235308-23def4e6c14b
	github.com/onsi/ginkgo v1.6.0
	github.com/onsi/gomega v0.0.0-20181121171407-65fb64232476
	golang.org/x/sys v0.0.0-20190215142949-d0b11bdaac8a // indirect
	google.golang.org/genproto v0.0.0-20170731182057-09f6ed296fc6 // indirect
	google.golang.org/grpc v0.0.0-20180619221905-168a6198bcb0
	k8s.io/kubernetes v0.0.0-20180627195542-91e7b4fd31fc
)
