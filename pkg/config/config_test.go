package config

import (
	"testing"
)

func TestParseFlags(t *testing.T) {
	// 这里我们将简单测试 Config 结构创建
	config := &Config{
		Mode:       "server",
		ListenPort: "8080",
		Key:        "test",
	}
	
	if config.Mode != "server" {
		t.Errorf("Expected Mode to be 'server', got %s", config.Mode)
	}
	
	if config.ListenPort != "8080" {
		t.Errorf("Expected ListenPort to be '8080', got %s", config.ListenPort)
	}
}

func TestValidate(t *testing.T) {
	// 测试有效的 server 配置
	config := &Config{
		Mode: "server",
		ListenPort: "8080",
	}
	
	if err := config.Validate(); err != nil {
		t.Errorf("Expected server config to be valid, got error: %v", err)
	}
	
	// 测试有效的 client 配置
	clientConfig := &Config{
		Mode: "client",
		ServerAddr: "ws://localhost:8080",
		TargetAddr: "127.0.0.1:3000",
		Key: "test",
	}
	
	if err := clientConfig.Validate(); err != nil {
		t.Errorf("Expected client config to be valid, got error: %v", err)
	}
	
	// 测试无效的模式
	invalidConfig := &Config{
		Mode: "invalid",
	}
	
	if err := invalidConfig.Validate(); err == nil {
		t.Error("Expected invalid mode to return error")
	}
	
	// 测试 client 缺少必须参数
	incompleteClient := &Config{
		Mode: "client",
		ServerAddr: "ws://localhost:8080",
		// TargetAddr 缺失
	}
	
	if err := incompleteClient.Validate(); err == nil {
		t.Error("Expected incomplete client config to return error")
	}
}