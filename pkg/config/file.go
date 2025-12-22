package config

import (
	"io/ioutil"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v2"
)

// FileConfig 用于YAML配置文件的结构
type FileConfig struct {
	Server ServerConfig `yaml:"server"`
	Client ClientConfig `yaml:"client"`
	Global GlobalConfig `yaml:"global"`
}

// ServerConfig 服务器配置
type ServerConfig struct {
	ListenPort   string `yaml:"listen_port"`
	CertFile     string `yaml:"cert_file"`
	KeyFile      string `yaml:"key_file"`
	IPRateLimit  int    `yaml:"ip_rate_limit"`
	KeyRateLimit int    `yaml:"key_rate_limit"`
}

// ClientConfig 客户端配置
type ClientConfig struct {
	ServerAddr string `yaml:"server_addr"`
	TargetAddr string `yaml:"target_addr"`
	Key        string `yaml:"key"`
	Insecure   bool   `yaml:"insecure"`
}

// GlobalConfig 全局配置
type GlobalConfig struct {
	LogLevel string `yaml:"log_level"`
	LogFile  string `yaml:"log_file"`
}

// LoadConfigFile 从YAML文件加载配置
func LoadConfigFile(filename string) (*FileConfig, error) {
	// 检查文件是否存在
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		return nil, err
	}

	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	var config FileConfig
	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return nil, err
	}

	return &config, nil
}

// SaveConfigFile 保存配置到YAML文件
func SaveConfigFile(filename string, config *FileConfig) error {
	// 创建目录（如果不存在）
	dir := filepath.Dir(filename)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := yaml.Marshal(config)
	if err != nil {
		return err
	}

	return ioutil.WriteFile(filename, data, 0644)
}

// MergeWithFileConfig 将文件配置合并到Config结构中
func (c *Config) MergeWithFileConfig(fileConfig *FileConfig, mode string) {
	// 合并全局配置
	if fileConfig.Global.LogLevel != "" {
		// LogLevel 在Config中还没有，暂时忽略
	}

	if mode == "server" {
		// 合并服务器配置（只有当命令行参数为默认值时才使用文件配置）
		if c.ListenPort == "443" && fileConfig.Server.ListenPort != "" {
			c.ListenPort = fileConfig.Server.ListenPort
		}
		if c.CertFile == "" && fileConfig.Server.CertFile != "" {
			c.CertFile = fileConfig.Server.CertFile
		}
		if c.KeyFile == "" && fileConfig.Server.KeyFile != "" {
			c.KeyFile = fileConfig.Server.KeyFile
		}
		if c.IPRateLimit == 0 && fileConfig.Server.IPRateLimit != 0 {
			c.IPRateLimit = fileConfig.Server.IPRateLimit
		}
		if c.KeyRateLimit == 0 && fileConfig.Server.KeyRateLimit != 0 {
			c.KeyRateLimit = fileConfig.Server.KeyRateLimit
		}
	} else if mode == "client" {
		// 合并客户端配置
		if c.ServerAddr == "" && fileConfig.Client.ServerAddr != "" {
			c.ServerAddr = fileConfig.Client.ServerAddr
		}
		if c.TargetAddr == "" && fileConfig.Client.TargetAddr != "" {
			c.TargetAddr = fileConfig.Client.TargetAddr
		}
		if c.Key == "default" && fileConfig.Client.Key != "" {
			c.Key = fileConfig.Client.Key
		}
		if !c.Insecure && fileConfig.Client.Insecure {
			c.Insecure = fileConfig.Client.Insecure
		}
	}
}

// LoadWithFile 加载配置，支持从文件读取
func LoadWithFile(configPath string, baseConfig *Config) (*Config, error) {
	// 使用传入的基础配置（已解析命令行参数）
	config := baseConfig

	// 如果指定了配置文件，则加载并合并
	if configPath != "" {
		fileConfig, err := LoadConfigFile(configPath)
		if err != nil {
			return nil, err
		}
		config.MergeWithFileConfig(fileConfig, config.Mode)
	} else {
		// 尝试在常见位置查找配置文件
		possiblePaths := []string{
			"./singleproxy.yaml",
			"./config/singleproxy.yaml", 
			"~/.singleproxy.yaml",
			"/etc/singleproxy.yaml",
		}

		for _, path := range possiblePaths {
			// 展开用户目录
			if path[0] == '~' {
				home, err := os.UserHomeDir()
				if err == nil {
					path = filepath.Join(home, path[1:])
				}
			}

			if fileConfig, err := LoadConfigFile(path); err == nil {
				config.MergeWithFileConfig(fileConfig, config.Mode)
				break
			}
		}
	}

	return config, nil
}

// GenerateExampleConfig 生成示例配置文件
func GenerateExampleConfig(filename string) error {
	exampleConfig := &FileConfig{
		Server: ServerConfig{
			ListenPort:   "443",
			CertFile:     "/path/to/cert.pem",
			KeyFile:      "/path/to/key.pem",
			IPRateLimit:  100,
			KeyRateLimit: 50,
		},
		Client: ClientConfig{
			ServerAddr: "wss://your-domain.com",
			TargetAddr: "127.0.0.1:8080",
			Key:        "your-service-key",
			Insecure:   false,
		},
		Global: GlobalConfig{
			LogLevel: "info",
			LogFile:  "/var/log/singleproxy.log",
		},
	}

	return SaveConfigFile(filename, exampleConfig)
}