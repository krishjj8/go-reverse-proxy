package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type ServerConfig struct {
	ListenAddress string `yaml:"listen_address"`
}

type RouteConfig struct {
	Upstreams []string `yaml:"upstreams"`
}

type Config struct {
	Server ServerConfig           `yaml:"server"`
	Routes map[string]RouteConfig `yaml:"routes"`
}

func LoadConfig(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err

	}

	defer file.Close()

	var cfg Config

	decoder := yaml.NewDecoder(file)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
