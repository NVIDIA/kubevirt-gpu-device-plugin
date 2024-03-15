package device_plugin

import (
	"fmt"
	"log"
	"os"

	"sigs.k8s.io/yaml"
)

const (
	DefaultConfigFilePath = "/etc/kubevirt-gpu-device-plugin/config.yaml"
)

type Config struct {
	GPUAliases []GPUAlias `json:"GPUAliases"`
}

type GPUAlias struct {
	GPUName string `json:"GPUName"`
	Alias   string `json:"alias"`
}

func LoadConfig(configFilePath string) (*Config, error) {
	var config Config

	if configFilePath == "" {
		configFilePath = DefaultConfigFilePath
	}

	configData, err := os.ReadFile(configFilePath)
	if err != nil {
		if os.IsNotExist(err) && configFilePath == DefaultConfigFilePath {
			log.Printf("warning: failed to read config file at default location: %v", err)
			return &config, nil
		}
		return nil, fmt.Errorf("failed to read config file: %v", err)
	}

	if err = yaml.Unmarshal(configData, &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %v", err)
	}

	return &config, nil
}
