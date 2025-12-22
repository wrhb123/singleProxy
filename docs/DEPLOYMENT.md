# Single Proxy 部署指南

## 系统要求

### 最低要求
- **操作系统**: Linux, Windows, macOS
- **内存**: 512MB RAM
- **存储**: 100MB 可用空间
- **网络**: 稳定的互联网连接

### 推荐配置
- **操作系统**: Ubuntu 20.04+ / CentOS 8+ / Windows Server 2019+
- **内存**: 2GB RAM
- **CPU**: 2 核心
- **存储**: 1GB 可用空间
- **带宽**: 100Mbps+

## 快速开始

### 1. 下载二进制文件

**从 GitHub Releases 下载**
```bash
# Linux AMD64
wget https://github.com/your-org/single-proxy/releases/latest/download/singleproxy-linux-amd64.tar.gz
tar -xzf singleproxy-linux-amd64.tar.gz

# Windows AMD64
# 下载 singleproxy-windows-amd64.zip 并解压

# macOS AMD64
wget https://github.com/your-org/single-proxy/releases/latest/download/singleproxy-darwin-amd64.tar.gz
tar -xzf singleproxy-darwin-amd64.tar.gz
```

**或从源码编译**
```bash
git clone https://github.com/your-org/single-proxy.git
cd single-proxy
go build -o singleproxy ./cmd/singleproxy
```

### 2. 生成配置文件

```bash
./singleproxy -generate-config
```

这将创建 `singleproxy.yaml` 示例配置文件。

### 3. 配置和启动

**编辑配置文件**
```yaml
server:
  listen_port: "443"
  cert_file: "/path/to/your/cert.pem"
  key_file: "/path/to/your/key.pem"
  ip_rate_limit: 100
  key_rate_limit: 50

global:
  log_level: "info"
  log_file: "/var/log/singleproxy.log"
```

**启动服务器**
```bash
./singleproxy -config=singleproxy.yaml -mode=server
```

## 生产环境部署

### 1. SSL 证书配置

**使用 Let's Encrypt（推荐）**
```bash
# 安装 Certbot
sudo apt-get install certbot

# 获取证书
sudo certbot certonly --standalone -d your-domain.com

# 证书路径
# 证书: /etc/letsencrypt/live/your-domain.com/fullchain.pem
# 私钥: /etc/letsencrypt/live/your-domain.com/privkey.pem
```

**配置自动续期**
```bash
# 添加到 crontab
echo "0 12 * * * /usr/bin/certbot renew --quiet" | sudo crontab -
```

### 2. 服务器配置

**创建专用用户**
```bash
sudo useradd --system --shell /bin/false singleproxy
```

**创建配置和日志目录**
```bash
sudo mkdir -p /etc/singleproxy
sudo mkdir -p /var/log/singleproxy
sudo chown singleproxy:singleproxy /var/log/singleproxy
```

**生产配置文件 `/etc/singleproxy/config.yaml`**
```yaml
server:
  listen_port: "443"
  cert_file: "/etc/letsencrypt/live/your-domain.com/fullchain.pem"
  key_file: "/etc/letsencrypt/live/your-domain.com/privkey.pem"
  ip_rate_limit: 100    # 根据需求调整
  key_rate_limit: 50    # 根据需求调整

global:
  log_level: "info"
  log_file: "/var/log/singleproxy/server.log"
```

### 3. Systemd 服务配置

**创建服务文件 `/etc/systemd/system/singleproxy.service`**
```ini
[Unit]
Description=Single Proxy Server
Documentation=https://github.com/your-org/single-proxy
After=network.target
Wants=network.target

[Service]
Type=simple
User=singleproxy
Group=singleproxy
ExecStart=/usr/local/bin/singleproxy -config=/etc/singleproxy/config.yaml -mode=server
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=5
StandardOutput=journal
StandardError=journal
SyslogIdentifier=singleproxy

# 安全设置
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/log/singleproxy
PrivateTmp=true
PrivateDevices=true
ProtectHostname=true
ProtectClock=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
RestrictNamespaces=true
LockPersonality=true
MemoryDenyWriteExecute=true
RestrictRealtime=true
RestrictSUIDSGID=true

# 资源限制
LimitNOFILE=65536
LimitNPROC=4096

[Install]
WantedBy=multi-user.target
```

**启动服务**
```bash
sudo systemctl daemon-reload
sudo systemctl enable singleproxy
sudo systemctl start singleproxy
```

**检查状态**
```bash
sudo systemctl status singleproxy
sudo journalctl -u singleproxy -f
```

### 4. 防火墙配置

**UFW (Ubuntu)**
```bash
sudo ufw allow 22/tcp     # SSH
sudo ufw allow 443/tcp    # HTTPS/WSS
sudo ufw allow 80/tcp     # HTTP (可选，用于证书验证)
sudo ufw enable
```

**iptables**
```bash
# 清除现有规则
sudo iptables -F

# 默认策略
sudo iptables -P INPUT DROP
sudo iptables -P FORWARD DROP
sudo iptables -P OUTPUT ACCEPT

# 允许本地回环
sudo iptables -A INPUT -i lo -j ACCEPT

# 允许已建立的连接
sudo iptables -A INPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT

# 允许必要端口
sudo iptables -A INPUT -p tcp --dport 22 -j ACCEPT   # SSH
sudo iptables -A INPUT -p tcp --dport 443 -j ACCEPT  # HTTPS
sudo iptables -A INPUT -p tcp --dport 80 -j ACCEPT   # HTTP

# 保存规则
sudo iptables-save | sudo tee /etc/iptables/rules.v4
```

## Docker 部署

### 1. 使用官方镜像

**拉取镜像**
```bash
docker pull your-org/single-proxy:latest
```

**运行容器**
```bash
docker run -d \
  --name singleproxy-server \
  -p 443:443 \
  -v /etc/letsencrypt:/etc/letsencrypt:ro \
  -v /var/log/singleproxy:/var/log/singleproxy \
  -v /etc/singleproxy:/etc/singleproxy:ro \
  your-org/single-proxy:latest \
  -config=/etc/singleproxy/config.yaml -mode=server
```

### 2. Docker Compose

**docker-compose.yml**
```yaml
version: '3.8'

services:
  singleproxy:
    image: your-org/single-proxy:latest
    container_name: singleproxy-server
    restart: unless-stopped
    ports:
      - "443:443"
      - "80:80"  # 可选，用于 HTTP 重定向
    volumes:
      - /etc/letsencrypt:/etc/letsencrypt:ro
      - /var/log/singleproxy:/var/log/singleproxy
      - ./config:/etc/singleproxy:ro
    command: ["-config=/etc/singleproxy/config.yaml", "-mode=server"]
    environment:
      - GOGC=100
      - GOMEMLIMIT=512MB
    security_opt:
      - no-new-privileges:true
    cap_drop:
      - ALL
    cap_add:
      - NET_BIND_SERVICE
    read_only: true
    tmpfs:
      - /tmp
    healthcheck:
      test: ["CMD", "curl", "-f", "https://localhost:443/health"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 40s
```

**启动服务**
```bash
docker-compose up -d
```

### 3. 构建自定义镜像

**Dockerfile**
```dockerfile
# 多阶段构建
FROM golang:1.21-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o singleproxy ./cmd/singleproxy

# 运行时镜像
FROM alpine:latest

RUN apk --no-cache add ca-certificates curl
WORKDIR /root/

# 创建非特权用户
RUN addgroup -g 1001 -S singleproxy && \
    adduser -u 1001 -S singleproxy -G singleproxy

COPY --from=builder /app/singleproxy .
RUN chmod +x singleproxy

# 切换到非特权用户
USER singleproxy

EXPOSE 443

ENTRYPOINT ["./singleproxy"]
```

**构建和推送**
```bash
docker build -t your-org/single-proxy:latest .
docker push your-org/single-proxy:latest
```

## Kubernetes 部署

### 1. Helm Chart 部署（推荐）

**添加 Helm 仓库**
```bash
helm repo add single-proxy https://your-org.github.io/single-proxy-helm
helm repo update
```

**安装**
```bash
helm install single-proxy single-proxy/single-proxy \
  --set server.domain=your-domain.com \
  --set server.cert.email=your-email@example.com \
  --set server.replicas=3
```

### 2. 原生 Kubernetes 清单

**namespace.yaml**
```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: single-proxy
```

**configmap.yaml**
```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: singleproxy-config
  namespace: single-proxy
data:
  config.yaml: |
    server:
      listen_port: "443"
      cert_file: "/etc/certs/tls.crt"
      key_file: "/etc/certs/tls.key"
      ip_rate_limit: 100
      key_rate_limit: 50
    global:
      log_level: "info"
      log_format: "json"
```

**deployment.yaml**
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: singleproxy
  namespace: single-proxy
spec:
  replicas: 3
  selector:
    matchLabels:
      app: singleproxy
  template:
    metadata:
      labels:
        app: singleproxy
    spec:
      containers:
      - name: singleproxy
        image: your-org/single-proxy:latest
        ports:
        - containerPort: 443
          name: https
        args:
          - "-config=/etc/singleproxy/config.yaml"
          - "-mode=server"
        volumeMounts:
        - name: config
          mountPath: /etc/singleproxy
        - name: certs
          mountPath: /etc/certs
        resources:
          requests:
            memory: "256Mi"
            cpu: "100m"
          limits:
            memory: "512Mi"
            cpu: "500m"
        livenessProbe:
          httpGet:
            path: /health
            port: 443
            scheme: HTTPS
          initialDelaySeconds: 30
          periodSeconds: 10
        readinessProbe:
          httpGet:
            path: /ready
            port: 443
            scheme: HTTPS
          initialDelaySeconds: 5
          periodSeconds: 5
      volumes:
      - name: config
        configMap:
          name: singleproxy-config
      - name: certs
        secret:
          secretName: singleproxy-tls
```

**service.yaml**
```yaml
apiVersion: v1
kind: Service
metadata:
  name: singleproxy
  namespace: single-proxy
spec:
  selector:
    app: singleproxy
  ports:
  - name: https
    port: 443
    targetPort: 443
  type: LoadBalancer
```

**ingress.yaml**（如果使用 Ingress）
```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: singleproxy
  namespace: single-proxy
  annotations:
    cert-manager.io/cluster-issuer: "letsencrypt-prod"
    nginx.ingress.kubernetes.io/websocket-services: "singleproxy"
    nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
    nginx.ingress.kubernetes.io/proxy-send-timeout: "3600"
spec:
  tls:
  - hosts:
    - your-domain.com
    secretName: singleproxy-tls
  rules:
  - host: your-domain.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: singleproxy
            port:
              number: 443
```

**应用清单**
```bash
kubectl apply -f namespace.yaml
kubectl apply -f configmap.yaml
kubectl apply -f deployment.yaml
kubectl apply -f service.yaml
kubectl apply -f ingress.yaml
```

## 客户端部署

### 1. 系统服务（Linux）

**创建配置文件 `/etc/singleproxy/client.yaml`**
```yaml
client:
  server_addr: "wss://your-domain.com"
  target_addr: "127.0.0.1:8080"
  key: "my-service-key"
  insecure: false

global:
  log_level: "info"
  log_file: "/var/log/singleproxy/client.log"
```

**创建服务文件 `/etc/systemd/system/singleproxy-client.service`**
```ini
[Unit]
Description=Single Proxy Client
After=network.target

[Service]
Type=simple
User=singleproxy
ExecStart=/usr/local/bin/singleproxy -config=/etc/singleproxy/client.yaml -mode=client
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

**启动客户端**
```bash
sudo systemctl daemon-reload
sudo systemctl enable singleproxy-client
sudo systemctl start singleproxy-client
```

### 2. Docker 客户端

```bash
docker run -d \
  --name singleproxy-client \
  --restart unless-stopped \
  -v /path/to/client-config.yaml:/etc/singleproxy/config.yaml:ro \
  your-org/single-proxy:latest \
  -config=/etc/singleproxy/config.yaml -mode=client
```

### 3. Windows 服务

**安装为 Windows 服务**
```cmd
# 使用 NSSM 或其他服务管理工具
nssm install SingleProxy "C:\path\to\singleproxy.exe"
nssm set SingleProxy Arguments "-config=C:\path\to\config.yaml -mode=client"
nssm set SingleProxy Start SERVICE_AUTO_START
nssm start SingleProxy
```

## 监控和运维

### 1. 日志管理

**日志轮转配置 `/etc/logrotate.d/singleproxy`**
```
/var/log/singleproxy/*.log {
    daily
    rotate 30
    compress
    delaycompress
    missingok
    notifempty
    create 644 singleproxy singleproxy
    postrotate
        systemctl reload singleproxy
    endscript
}
```

### 2. 监控集成

**Prometheus 配置**
```yaml
# prometheus.yml
scrape_configs:
  - job_name: 'singleproxy'
    static_configs:
      - targets: ['your-domain.com:443']
    metrics_path: /metrics
    scheme: https
```

**Grafana 仪表板**
- 连接数监控
- 流量统计
- 错误率分析
- 响应时间分布

### 3. 健康检查

**健康检查脚本**
```bash
#!/bin/bash
# /usr/local/bin/check_singleproxy.sh

HEALTH_URL="https://your-domain.com/health"
TIMEOUT=10

if curl -f -s --max-time $TIMEOUT $HEALTH_URL > /dev/null; then
    echo "OK: Single Proxy is healthy"
    exit 0
else
    echo "CRITICAL: Single Proxy is not responding"
    exit 2
fi
```

**Cron 监控**
```bash
# 每分钟检查一次
* * * * * /usr/local/bin/check_singleproxy.sh
```

## 安全加固

### 1. 网络安全

**Fail2ban 配置 `/etc/fail2ban/jail.local`**
```ini
[singleproxy]
enabled = true
filter = singleproxy
logpath = /var/log/singleproxy/*.log
maxretry = 5
bantime = 3600
findtime = 600
```

**Fail2ban 过滤器 `/etc/fail2ban/filter.d/singleproxy.conf`**
```ini
[Definition]
failregex = level=ERROR.*client_ip=<HOST>.*rate_limited
ignoreregex =
```

### 2. 访问控制

**IP 白名单**
```bash
# 仅允许特定IP访问
iptables -A INPUT -p tcp --dport 443 -s 192.168.1.0/24 -j ACCEPT
iptables -A INPUT -p tcp --dport 443 -j DROP
```

**客户端认证**
```yaml
# 使用强密钥
client:
  key: "$(openssl rand -hex 32)"
```

### 3. 定期维护

**自动更新脚本**
```bash
#!/bin/bash
# /usr/local/bin/update_singleproxy.sh

CURRENT_VERSION=$(./singleproxy -version 2>&1 | grep -oP '\d+\.\d+\.\d+')
LATEST_VERSION=$(curl -s https://api.github.com/repos/your-org/single-proxy/releases/latest | jq -r .tag_name | sed 's/v//')

if [ "$CURRENT_VERSION" != "$LATEST_VERSION" ]; then
    echo "Updating from $CURRENT_VERSION to $LATEST_VERSION"
    # 下载新版本
    wget -O /tmp/singleproxy https://github.com/your-org/single-proxy/releases/latest/download/singleproxy-linux-amd64
    # 停止服务
    systemctl stop singleproxy
    # 备份当前版本
    cp /usr/local/bin/singleproxy /usr/local/bin/singleproxy.backup
    # 安装新版本
    mv /tmp/singleproxy /usr/local/bin/singleproxy
    chmod +x /usr/local/bin/singleproxy
    # 重启服务
    systemctl start singleproxy
    echo "Update completed"
fi
```

## 故障排除

### 常见问题和解决方案

**1. 服务启动失败**
```bash
# 检查配置文件
./singleproxy -config=/path/to/config.yaml -mode=server -log-level=debug

# 检查端口占用
netstat -tulpn | grep :443

# 检查文件权限
ls -la /etc/singleproxy/
```

**2. 证书问题**
```bash
# 验证证书
openssl x509 -in /path/to/cert.pem -text -noout

# 检查证书链
openssl verify -CAfile /path/to/ca.pem /path/to/cert.pem
```

**3. 连接问题**
```bash
# 测试 WebSocket 连接
websocat wss://your-domain.com/ws/test

# 检查网络连通性
telnet your-domain.com 443
```

### 日志分析

**查看错误日志**
```bash
# 查看最近的错误
journalctl -u singleproxy | grep ERROR

# 实时监控日志
tail -f /var/log/singleproxy/server.log | jq 'select(.level == "ERROR")'
```

**性能分析**
```bash
# 查看连接统计
grep "tunnel_connected" /var/log/singleproxy/server.log | wc -l

# 分析请求延迟
grep "response_time" /var/log/singleproxy/server.log | jq -r '.response_time' | sort -n
```

## 备份和恢复

### 配置备份
```bash
#!/bin/bash
# 备份配置文件
tar -czf /backup/singleproxy-config-$(date +%Y%m%d).tar.gz \
  /etc/singleproxy/ \
  /etc/systemd/system/singleproxy*.service
```

### 灾难恢复
```bash
# 恢复配置
tar -xzf /backup/singleproxy-config-20231222.tar.gz -C /

# 重新加载服务
systemctl daemon-reload
systemctl enable singleproxy
systemctl start singleproxy
```

---

## 支持和反馈

- **文档**: 查看项目 Wiki
- **问题报告**: 提交 GitHub Issue
- **功能请求**: GitHub Discussions
- **安全问题**: 发送邮件至 security@your-org.com