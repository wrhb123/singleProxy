# SingleProxy 使用演示

## 概述

优化后的 SingleProxy 在单个端口上支持两种功能：
1. **SOCKS5 代理** - 替代了原有的HTTP正向代理
2. **WebSocket 内网穿透** - 保持原有功能

## 功能特点

### ✅ 改进点

- **单端口多协议**：自动检测SOCKS5和HTTP协议
- **标准SOCKS5实现**：替换了有问题的HTTP正向代理
- **保留内网穿透**：WebSocket隧道功能完全保留
- **更好的兼容性**：SOCKS5是标准代理协议，支持更多客户端

### 🚀 架构优势

- **KISS原则**：简化了代理实现逻辑
- **DRY原则**：消除了重复的连接处理代码
- **SOLID原则**：清晰的职责分离

## 使用方法

### 1. 启动服务器

```bash
# 启动服务器 (支持SOCKS5代理 + WebSocket隧道)
./singleproxy -mode=server -port=8080

# 带TLS启动
./singleproxy -mode=server -port=443 -cert=cert.pem -key-file=key.pem
```

### 2. SOCKS5 代理使用

```bash
# 使用curl通过SOCKS5代理
curl -x socks5://127.0.0.1:8080 http://httpbin.org/ip

# 使用SSH通过SOCKS5代理
ssh -o ProxyCommand='nc -x 127.0.0.1:8080 %h %p' user@target.com

# Firefox浏览器配置
# 网络设置 -> SOCKS5代理: 127.0.0.1:8080
```

### 3. 内网穿透使用

```bash
# 启动内网客户端（将本地8081端口暴露到公网）
./singleproxy -mode=client -server=ws://yourdomain.com:8080 -target=127.0.0.1:8081 -key=myapp

# 公网访问内网服务
curl -H "X-Tunnel-Key: myapp" http://yourdomain.com:8080/
```

## 故障排除

### 🔧 连接失败诊断

如果遇到 "connection reset by peer" 错误，请按以下步骤排查：

1. **运行诊断脚本**：
```bash
./diagnose.sh
```

2. **检查服务器日志**：
- 查看是否有 "Detected HTTP protocol" 日志
- 检查是否有协议检测错误

3. **常见问题和解决方案**：

**问题1**: WebSocket握手失败
```
解决方案:
- 确保服务器URL格式正确: ws://host:port 或 wss://host:port
- 检查防火墙是否允许WebSocket连接
- 验证服务器是否正确响应WebSocket升级请求
```

**问题2**: SOCKS5代理无法连接
```
解决方案:
- 测试: curl -x socks5://server:port http://httpbin.org/ip
- 检查是否有其他程序占用端口
- 验证SOCKS5客户端配置正确
```

**问题3**: 协议检测错误
```
解决方案:
- 查看服务器日志中的协议检测信息
- 确保客户端发送的第一个字节正确
- 检查网络中间设备是否修改了数据包
```

## 协议检测机制

服务器通过读取连接的前16个字节来判断协议类型：
- **第一字节 = 0x05** → SOCKS5协议
- **其他** → HTTP协议（WebSocket升级或普通HTTP）

## 性能优势

1. **内存效率**：去除了重复的HTTP代理处理逻辑
2. **连接复用**：单端口处理多种协议
3. **标准兼容**：SOCKS5支持更广泛的应用程序

## 测试方法

```bash
# 运行测试（需要先启动服务器）
go test -v

# 测试SOCKS5代理功能
go test -run TestSOCKS5ProxyForwarding -v
```

## 调试模式

启动服务器时会显示详细的协议检测日志：

```bash
2025/12/22 10:53:54 Server listening without TLS on port 8080
2025/12/22 10:53:54 Server supports: HTTP/WebSocket tunneling and SOCKS5 proxy
2025/12/22 10:53:55 Detected HTTP protocol (first bytes: "GET /ws/t")
```

这些日志帮助诊断连接和协议识别问题。