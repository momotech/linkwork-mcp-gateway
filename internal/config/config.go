package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server     ServerConfig     `yaml:"server"`
	WebService WebServiceConfig `yaml:"webService"`
	DNS        DNSConfig        `yaml:"dns"`
	Redis      RedisConfig      `yaml:"redis"`
	Encryption EncryptionConfig `yaml:"encryption"`
	Proxy      ProxyConfig      `yaml:"proxy"`
}

type ServerConfig struct {
	Port int `yaml:"port"`
}

type WebServiceConfig struct {
	BaseURL              string        `yaml:"baseUrl"`
	RegistrySyncInterval time.Duration `yaml:"registrySyncInterval"`
	TaskValidateTTL      time.Duration `yaml:"taskValidateTTL"`
}

type DNSConfig struct {
	Internal     string            `yaml:"internal"`
	Office       string            `yaml:"office"`
	VirtualHosts map[string]string `yaml:"virtualHosts"`
}

type RedisConfig struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}

type EncryptionConfig struct {
	KeyEnv string `yaml:"keyEnv"`
}

type ProxyConfig struct {
	SSETimeout     time.Duration `yaml:"sseTimeout"`
	MaxRequestBody int64         `yaml:"maxRequestBody"`
}

func Load(path string) (*Config, error) {
	cfg := &Config{
		Server:     ServerConfig{Port: 9080},
		WebService: WebServiceConfig{RegistrySyncInterval: 60 * time.Second, TaskValidateTTL: 2 * time.Hour},
		DNS:        DNSConfig{Internal: "", Office: ""},
		Redis:      RedisConfig{Addr: "127.0.0.1:6379"},
		Proxy:      ProxyConfig{SSETimeout: 5 * time.Minute, MaxRequestBody: 10 << 20},
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if envAddr := os.Getenv("REDIS_ADDR"); envAddr != "" {
		cfg.Redis.Addr = envAddr
	}
	if envWS := os.Getenv("WEB_SERVICE_BASE_URL"); envWS != "" {
		cfg.WebService.BaseURL = envWS
	}
	if envPort := os.Getenv("GATEWAY_PORT"); envPort != "" {
		var p int
		if _, err := fmt.Sscanf(envPort, "%d", &p); err == nil && p > 0 {
			cfg.Server.Port = p
		}
	}

	return cfg, nil
}
