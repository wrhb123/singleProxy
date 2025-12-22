package logger

import (
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"singleproxy/pkg/config"
)

// Logger 包装slog.Logger提供更方便的接口
type Logger struct {
	*slog.Logger
	level slog.Level
}

// Global logger instance
var globalLogger *Logger

// InitLogger 初始化全局日志器
func InitLogger(cfg *config.Config) error {
	var writer io.Writer = os.Stdout

	// 如果指定了日志文件，创建文件写入器
	if cfg.LogFile != "" {
		// 创建日志目录
		if err := os.MkdirAll(filepath.Dir(cfg.LogFile), 0755); err != nil {
			return err
		}

		file, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return err
		}
		writer = file
	}

	// 解析日志级别
	level := parseLogLevel(cfg.LogLevel)

	// 创建处理器
	var handler slog.Handler
	opts := &slog.HandlerOptions{
		Level: level,
	}

	switch strings.ToLower(cfg.LogFormat) {
	case "json":
		handler = slog.NewJSONHandler(writer, opts)
	default:
		handler = slog.NewTextHandler(writer, opts)
	}

	// 创建并设置全局日志器
	slogLogger := slog.New(handler)
	globalLogger = &Logger{
		Logger: slogLogger,
		level:  level,
	}

	// 设置标准库log也使用我们的日志器
	log.SetOutput(io.Discard) // 禁用标准log输出

	return nil
}

// parseLogLevel 解析日志级别字符串
func parseLogLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// GetLogger 获取全局日志器
func GetLogger() *Logger {
	if globalLogger == nil {
		// 如果没有初始化，创建一个默认的文本日志器
		handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		})
		globalLogger = &Logger{
			Logger: slog.New(handler),
			level:  slog.LevelInfo,
		}
	}
	return globalLogger
}

// 便捷方法
func Debug(msg string, args ...any) {
	GetLogger().Debug(msg, args...)
}

func Info(msg string, args ...any) {
	GetLogger().Info(msg, args...)
}

func Warn(msg string, args ...any) {
	GetLogger().Warn(msg, args...)
}

func Error(msg string, args ...any) {
	GetLogger().Error(msg, args...)
}

// WithFields 创建带有字段的日志器
func (l *Logger) WithFields(fields map[string]any) *Logger {
	args := make([]any, 0, len(fields)*2)
	for k, v := range fields {
		args = append(args, k, v)
	}
	return &Logger{
		Logger: l.With(args...),
		level:  l.level,
	}
}

// WithField 创建带有单个字段的日志器
func (l *Logger) WithField(key string, value any) *Logger {
	return &Logger{
		Logger: l.With(key, value),
		level:  l.level,
	}
}

// 全局便捷方法，带字段
func WithFields(fields map[string]any) *Logger {
	return GetLogger().WithFields(fields)
}

func WithField(key string, value any) *Logger {
	return GetLogger().WithField(key, value)
}

// RequestLogger 为HTTP请求创建专用日志器
func RequestLogger(requestID string, clientIP string, method string, path string) *Logger {
	return GetLogger().WithFields(map[string]any{
		"request_id": requestID,
		"client_ip":  clientIP,
		"method":     method,
		"path":       path,
	})
}

// TunnelLogger 为隧道连接创建专用日志器
func TunnelLogger(key string, clientAddr string) *Logger {
	return GetLogger().WithFields(map[string]any{
		"tunnel_key":  key,
		"client_addr": clientAddr,
		"component":   "tunnel",
	})
}

// IsDebugEnabled 检查是否启用调试级别
func (l *Logger) IsDebugEnabled() bool {
	return l.level <= slog.LevelDebug
}

// IsInfoEnabled 检查是否启用信息级别
func (l *Logger) IsInfoEnabled() bool {
	return l.level <= slog.LevelInfo
}

// Fatal 记录致命错误并退出程序
func (l *Logger) Fatal(msg string, args ...any) {
	l.Error(msg, args...)
	os.Exit(1)
}

// Fatal 全局致命错误方法
func Fatal(msg string, args ...any) {
	GetLogger().Fatal(msg, args...)
}
