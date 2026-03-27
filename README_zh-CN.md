# linkwork-mcp-gateway

`linkwork-mcp-gateway` 是 LinkWork 的 MCP 网关服务，负责 MCP 服务发现缓存、协议代理、健康检查和调用用量记录。

## 接口概览

- `GET /healthz`：网关自检
- `GET /health/{name}`：单个 MCP 服务健康状态
- `GET /tools/{name}`：工具列表（缓存）
- `POST /proxy/{name}/mcp`：标准 MCP 代理
- 兼容接口：`/proxy/probe`、`/proxy/discover`、`/proxy/health`

默认端口：`9080`

## 本地开发

### 1) 环境要求

- Go 1.24.4+
- Redis（可选但建议）

### 2) 启动

```bash
cd linkwork-mcp-gateway
go run ./cmd/gateway -config config.yaml
```

### 3) 常用配置

`config.yaml` 字段：

- `server.port`
- `webService.baseUrl`
- `redis.addr/password/db`
- `proxy.sseTimeout`

环境变量可覆盖：

- `REDIS_ADDR`
- `WEB_SERVICE_BASE_URL`
- `GATEWAY_PORT`

## Deploy 流程

### 方案 A：Docker 部署

```bash
cd linkwork-mcp-gateway
docker build -t linkwork-mcp-gateway:latest .
docker run -d --name linkwork-mcp-gateway \
  -p 9080:9080 \
  -e WEB_SERVICE_BASE_URL=http://<linkwork-server>:8081 \
  -e REDIS_ADDR=<redis-host>:6379 \
  linkwork-mcp-gateway:latest
```

### 方案 B：Kubernetes 部署

仓库已提供示例清单：`k8s/deployment.yaml`

```bash
kubectl apply -f k8s/deployment.yaml
```

默认会创建：

- `Namespace`: `linkwork-mcp-gateway`
- `Deployment`: 2 副本
- `Service`: ClusterIP + NodePort(30890)

## 依赖关系

- 上游：`linkwork-server`（MCP 注册信息来源）
- 下游：各 MCP Server（SSE/HTTP/stdio）
- 中间态：Redis（用量与缓存）
