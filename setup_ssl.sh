#!/bin/bash

# 创建测试环境的自签名SSL证书
echo "🔐 生成测试用SSL证书..."

# 创建SSL目录
mkdir -p ssl

# 生成私钥
openssl genrsa -out ssl/test.key 2048

# 生成证书签名请求
openssl req -new -key ssl/test.key -out ssl/test.csr -subj "/C=CN/ST=Beijing/L=Beijing/O=Test/OU=Test/CN=test.example.com"

# 生成自签名证书
openssl x509 -req -days 365 -in ssl/test.csr -signkey ssl/test.key -out ssl/test.crt

# 清理临时文件
rm ssl/test.csr

echo "✅ SSL证书生成完成:"
echo "   - ssl/test.key (私钥)"
echo "   - ssl/test.crt (证书)"
echo ""

# 配置本地hosts
echo "📝 请添加以下内容到 /etc/hosts:"
echo "127.0.0.1    test.example.com"
echo ""

echo "🚀 使用方法:"
echo "1. 复制nginx.conf到nginx配置目录"
echo "2. 复制ssl/目录到nginx目录"
echo "3. 重启nginx服务"
echo "4. 运行测试脚本"