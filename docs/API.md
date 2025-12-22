# Single Proxy API 文档

## 概述

Single Proxy 是一个基于 WebSocket 的内网穿透工具，提供简单高效的方式将内网服务暴露到公网，同时支持 HTTP 正向代理功能。

## 架构图

```
Internet          Public Server          Internal Network
   |                    |                       |
[Client] ←--→ [Single Proxy Server] ←--→ [Single Proxy Client] ←--→ [Target Service]
   ↓                    ↓                       ↓
HTTP Request         WebSocket               HTTP Forward
                     Tunnel
```

## 核心概念

### 隧道密钥 (Tunnel Key)
- 用于区分不同的内网服务
- 通过 `X-Tunnel-Key` HTTP 头部指定
- 同一密钥只能有一个活跃连接

### 消息类型
- `MSG_TYPE_HTTP_REQ` (1): HTTP 请求
- `MSG_TYPE_HTTP_RES` (2): HTTP 响应头
- `MSG_TYPE_HTTP_RES_CHUNK` (3): HTTP 响应体数据块

## 配置参数

### 服务器模式配置

| 参数 | 类型 | 默认值 | 描述 |
|------|------|--------|------|
| `mode` | string | "server" | 运行模式 |
| `port` | string | "443" | 监听端口 |
| `cert` | string | "" | TLS 证书文件路径 |
| `key-file` | string | "" | TLS 私钥文件路径 |
| `ip-rate-limit` | int | 0 | IP 速率限制（请求/秒，0=无限制） |
| `key-rate-limit` | int | 0 | Key 速率限制（请求/秒，0=无限制） |

### 客户端模式配置

| 参数 | 类型 | 默认值 | 描述 |
|------|------|--------|------|
| `mode` | string | "client" | 运行模式 |
| `server` | string | "" | 服务器地址（如：wss://example.com） |
| `target` | string | "" | 目标服务地址（如：127.0.0.1:8080） |
| `key` | string | "default" | 隧道密钥 |
| `insecure` | bool | false | 跳过 TLS 证书验证 |

### 日志配置

| 参数 | 类型 | 默认值 | 描述 |
|------|------|--------|------|
| `log-level` | string | "info" | 日志级别：debug/info/warn/error |
| `log-format` | string | "text" | 日志格式：text/json |
| `log-file` | string | "" | 日志文件路径（空=stdout） |
| `config` | string | "" | YAML 配置文件路径 |

## HTTP API

### 隧道注册

**WebSocket 连接**
```
GET /ws/{tunnel_key}
Upgrade: websocket
Connection: Upgrade
```

**响应**
```
HTTP/1.1 101 Switching Protocols
Upgrade: websocket
Connection: Upgrade
```

### 公网请求代理

**请求格式**
```
GET|POST|PUT|DELETE|PATCH /{path}
Host: proxy-server.com
X-Tunnel-Key: {tunnel_key}
[其他HTTP头部...]
```

**响应格式**
```
HTTP/1.1 {status_code} {status_text}
Content-Type: {content_type}
[其他HTTP头部...]

{response_body}
```

### 错误响应

| HTTP 状态码 | 描述 | 原因 |
|------------|------|------|
| 400 | Bad Request | 隧道密钥为空 |
| 429 | Too Many Requests | 超出速率限制 |
| 502 | Bad Gateway | 无可用隧道连接 |
| 504 | Gateway Timeout | 隧道响应超时 |

## WebSocket 协议

### 消息格式

**二进制消息结构**
```
[ID:8字节][Type:1字节][Payload Length:4字节][Payload:N字节]
```

- **ID**: 请求唯一标识符（uint64，大端序）
- **Type**: 消息类型（uint8）
- **Payload Length**: 负载长度（uint32，大端序）
- **Payload**: 消息负载数据

### HTTP 请求序列化

```
METHOD {path} HTTP/1.1\r\n
Host: {host}\r\n
{header_name}: {header_value}\r\n
\r\n
{body}
```

### HTTP 响应序列化

```
HTTP/1.1 {status_code} {status_text}\r\n
{header_name}: {header_value}\r\n
\r\n
{body}
```

## 配置文件格式

### YAML 配置示例

```yaml
# 服务器配置
server:
  listen_port: "443"
  cert_file: "/path/to/cert.pem"
  key_file: "/path/to/key.pem"
  ip_rate_limit: 100     # 每个IP每秒100个请求
  key_rate_limit: 50     # 每个Key每秒50个请求

# 客户端配置
client:
  server_addr: "wss://your-domain.com"
  target_addr: "127.0.0.1:8080"
  key: "your-service-key"
  insecure: false        # 启用TLS证书验证

# 全局配置
global:
  log_level: "info"
  log_file: "/var/log/singleproxy.log"
```

### 配置优先级

1. **命令行参数**（最高优先级）
2. **配置文件参数**
3. **默认值**（最低优先级）

### 自动配置文件发现

系统会按以下顺序查找配置文件：
1. `./singleproxy.yaml`
2. `./config/singleproxy.yaml`
3. `~/.singleproxy.yaml`
4. `/etc/singleproxy.yaml`

## 使用示例

### 1. 基本 HTTP 隧道

**启动服务器**
```bash
./singleproxy -mode=server -port=443 -cert=/path/to/cert.pem -key-file=/path/to/key.pem
```

**启动客户端**
```bash
./singleproxy -mode=client -server="wss://your-domain.com" -target="127.0.0.1:8080" -key="web-service"
```

**访问服务**
```bash
curl -H "X-Tunnel-Key: web-service" https://your-domain.com/api/status
```

### 2. 多服务隧道

**启动多个客户端**
```bash
# API 服务
./singleproxy -mode=client -server="wss://your-domain.com" -target="127.0.0.1:3000" -key="api"

# Web 服务
./singleproxy -mode=client -server="wss://your-domain.com" -target="127.0.0.1:8080" -key="web"

# 数据库管理
./singleproxy -mode=client -server="wss://your-domain.com" -target="127.0.0.1:5432" -key="db-admin"
```

**访问不同服务**
```bash
# 访问 API
curl -H "X-Tunnel-Key: api" https://your-domain.com/users

# 访问 Web 界面
curl -H "X-Tunnel-Key: web" https://your-domain.com/

# 访问数据库管理
curl -H "X-Tunnel-Key: db-admin" https://your-domain.com/admin
```

### 3. 使用配置文件

**生成配置文件**
```bash
./singleproxy -generate-config
```

**使用配置文件启动**
```bash
./singleproxy -config=singleproxy.yaml -mode=server
```

### 4. 调试和监控

**启用调试日志**
```bash
./singleproxy -mode=server -log-level=debug -log-format=json
```

**查看连接状态**
```bash
# JSON 格式的结构化日志便于解析
./singleproxy -mode=server -log-format=json | jq 'select(.component == "tunnel")'
```

## SOCKS5 代理支持

除了 HTTP 隧道，Single Proxy 还支持 SOCKS5 代理协议。

**检测逻辑**
- 连接的第一个字节为 `0x05` → SOCKS5 协议
- 否则 → HTTP 协议

**使用示例**
```bash
# 通过代理服务器使用 SOCKS5
curl --socks5 your-domain.com:443 http://internal-service.local
```

## 安全考虑

### TLS/SSL
- 生产环境务必使用 HTTPS/WSS
- 使用有效的 SSL 证书
- 定期更新证书

### 访问控制
- 使用强密码作为隧道密钥
- 配置适当的速率限制
- 监控异常流量

### 防火墙配置
```bash
# 只允许必要端口
iptables -A INPUT -p tcp --dport 443 -j ACCEPT
iptables -A INPUT -p tcp --dport 80 -j ACCEPT
```

## 性能调优

### 连接数限制
- 默认支持 1000+ 并发连接
- 可通过系统参数调整：
```bash
echo "65536" > /proc/sys/fs/file-max
ulimit -n 65536
```

### 内存优化
- 流式传输避免大文件内存占用
- 空闲状态内存使用 < 100MB
- 启用 Go GC 调优：
```bash
export GOGC=100
export GOMEMLIMIT=512MB
```

### 网络优化
- TCP 快速打开：`echo 3 > /proc/sys/net/ipv4/tcp_fastopen`
- TCP BBR 拥塞控制：`echo bbr > /proc/sys/net/ipv4/tcp_congestion_control`

## 故障排除

### 常见问题

**连接失败**
```
ERROR: websocket: bad handshake
```
- 检查服务器地址和端口
- 验证 TLS 证书有效性
- 确认防火墙设置

**隧道断开**
```
ERROR: websocket: close 1006 (abnormal closure)
```
- 网络不稳定，客户端会自动重连
- 检查代理或防火墙配置
- 增加心跳超时时间

**速率限制**
```
HTTP 429 Too Many Requests
```
- 调整 IP 或 Key 速率限制
- 使用不同的隧道密钥分散负载

### 日志分析

**启用详细日志**
```bash
./singleproxy -log-level=debug -log-format=json
```

**关键日志事件**
- `tunnel_connected`: 隧道建立
- `tunnel_disconnected`: 隧道断开
- `request_forwarded`: 请求转发
- `rate_limited`: 速率限制触发

## API 版本

当前 API 版本：**v1.0**

## 更新历史

### v1.0.0
- 基础 HTTP 隧道功能
- SOCKS5 代理支持
- 速率限制
- TLS 支持
- 配置文件支持
- 结构化日志
- 健康监控

---

**技术支持**: 如有问题，请提交 Issue 或查看项目文档。