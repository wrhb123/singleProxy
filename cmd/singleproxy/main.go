package main

import (
	"flag"
	"os"

	"singleproxy/pkg/client"
	"singleproxy/pkg/config"
	"singleproxy/pkg/logger"
	"singleproxy/pkg/server"
)

func main() {
	// 先定义生成配置的flag
	generateConfig := flag.Bool("generate-config", false, "生成示例配置文件")

	// 解析命令行参数
	cfg := config.ParseFlags()

	// 如果用户请求生成示例配置，则生成并退出
	if *generateConfig {
		filename := "singleproxy.yaml"
		if err := config.GenerateExampleConfig(filename); err != nil {
			logger.Fatal("生成配置文件失败", "error", err)
		}
		logger.Info("示例配置文件已生成", "file", filename)
		os.Exit(0)
	}

	// 如果指定了配置文件或存在默认配置文件，则加载
	if cfg.ConfigFile != "" {
		loadedCfg, err := config.LoadWithFile(cfg.ConfigFile, cfg)
		if err != nil {
			logger.Fatal("加载配置文件失败", "file", cfg.ConfigFile, "error", err)
		}
		cfg = loadedCfg
	} else {
		// 尝试自动发现配置文件
		loadedCfg, err := config.LoadWithFile("", cfg)
		if err == nil {
			cfg = loadedCfg
		}
	}

	// 初始化日志系统
	if err := logger.InitLogger(cfg); err != nil {
		logger.Fatal("初始化日志系统失败", "error", err)
	}

	// 验证配置
	if err := cfg.Validate(); err != nil {
		logger.Fatal("配置验证失败", "error", err)
	}

	logger.Info("应用启动",
		"mode", cfg.Mode,
		"log_level", cfg.LogLevel,
		"log_format", cfg.LogFormat)

	// 根据模式启动相应服务
	if cfg.Mode == "server" {
		srv := server.NewSinglePortProxy(cfg)
		logger.Info("启动服务器", "port", cfg.ListenPort)
		if err := srv.Start(); err != nil {
			logger.Fatal("服务器启动失败", "error", err)
		}
	} else {
		cli, err := client.NewTunnelClient(cfg)
		if err != nil {
			logger.Fatal("创建客户端失败", "error", err)
		}

		logger.Info("启动客户端",
			"server", cfg.ServerAddr,
			"target", cfg.TargetAddr,
			"key", cfg.Key)

		cli.Run()
	}
}
