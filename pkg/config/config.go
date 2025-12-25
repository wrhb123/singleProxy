package config

import (
	"flag"
	"fmt"
)

// Config 结构体用于存储应用程序配置
type Config struct {
	Mode       string // "server" or "client"
	ListenPort string // Server listening port
	ServerAddr string // Server address for client to connect to (e.g., wss://example.com:443)
	TargetAddr string // Target service address for client to forward to (e.g., 127.0.0.1:8080)
	Key        string // Tunnel key for identifying the service
	CertFile   string // TLS cert file for server
	KeyFile    string // TLS key file for server
	Insecure   bool   // Skip TLS certificate verification for client

	IPRateLimit  int // 每个IP每秒的请求限制
	KeyRateLimit int // 每个key每秒的请求限制

	// 日志配置
	LogLevel    string // 日志级别: debug, info, warn, error
	LogFile     string // 日志文件路径
	LogFormat   string // 日志格式: text, json
	ConfigFile  string // 配置文件路径
}

// ParseFlags 解析命令行参数
func ParseFlags() *Config {
	config := &Config{}
	flag.StringVar(&config.Mode, "mode", "server", "运行模式: server, client, 或 http-client")
	flag.StringVar(&config.ListenPort, "port", "443", "服务器监听端口")
	flag.StringVar(&config.ServerAddr, "server", "", "服务器地址, e.g. wss://yourdomain.com (client模式)")
	flag.StringVar(&config.TargetAddr, "target", "", "目标服务地址, e.g. 127.0.0.1:8080 (client模式)")
	flag.StringVar(&config.Key, "key", "default", "隧道密钥")
	flag.StringVar(&config.CertFile, "cert", "", "TLS证书文件路径 (server模式)")
	flag.StringVar(&config.KeyFile, "key-file", "", "TLS私钥文件路径 (server模式)")
	flag.BoolVar(&config.Insecure, "insecure", false, "跳过TLS证书验证 (client模式)")
	flag.IntVar(&config.IPRateLimit, "ip-rate-limit", 0, "每个IP每秒的请求限制 (0为无限制)")
	flag.IntVar(&config.KeyRateLimit, "key-rate-limit", 0, "每个key每秒的请求限制 (0为无限制)")
	
	// 日志相关参数
	flag.StringVar(&config.LogLevel, "log-level", "info", "日志级别: debug, info, warn, error")
	flag.StringVar(&config.LogFile, "log-file", "", "日志文件路径 (空则输出到stdout)")
	flag.StringVar(&config.LogFormat, "log-format", "text", "日志格式: text, json")
	flag.StringVar(&config.ConfigFile, "config", "", "配置文件路径 (YAML格式)")

	flag.Parse()
	return config
}

// Validate 验证配置的有效性
func (c *Config) Validate() error {
	if c.Mode != "server" && c.Mode != "client" && c.Mode != "http-client" {
		return fmt.Errorf("错误: 模式必须是 'server'、'client' 或 'http-client'")
	}
	if c.Mode == "client" || c.Mode == "http-client" {
		if c.ServerAddr == "" || c.TargetAddr == "" {
			return fmt.Errorf("错误: %s模式需要指定 -server 和 -target 参数", c.Mode)
		}
	}
	return nil
}