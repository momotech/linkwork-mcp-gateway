# linkwork-mcp-gateway

English | [中文](./README_zh-CN.md)

`linkwork-mcp-gateway` is the LinkWork MCP gateway service. It provides MCP registry cache, protocol proxy, health checks, and usage recording.

## API Overview

- `GET /healthz`: gateway self health
- `GET /health/{name}`: per-MCP health status
- `GET /tools/{name}`: tool list (cached)
- `POST /proxy/{name}/mcp`: standard MCP proxy route
- Compatibility routes: `/proxy/probe`, `/proxy/discover`, `/proxy/health`

Default port: `9080`

## Local Development

### 1) Requirements

- Go 1.24.4+
- Redis (optional but recommended)

### 2) Run

```bash
cd linkwork-mcp-gateway
go run ./cmd/gateway -config config.yaml
```

### 3) Common configuration

`config.yaml` includes:

- `server.port`
- `webService.baseUrl`
- `redis.addr/password/db`
- `proxy.sseTimeout`

Environment overrides:

- `REDIS_ADDR`
- `WEB_SERVICE_BASE_URL`
- `GATEWAY_PORT`

## Deploy Flow

### Option A: Docker deployment

```bash
cd linkwork-mcp-gateway
docker build -t linkwork-mcp-gateway:latest .
docker run -d --name linkwork-mcp-gateway \
  -p 9080:9080 \
  -e WEB_SERVICE_BASE_URL=http://<linkwork-server>:8081 \
  -e REDIS_ADDR=<redis-host>:6379 \
  linkwork-mcp-gateway:latest
```

### Option B: Kubernetes deployment

Example manifest is included: `k8s/deployment.yaml`

```bash
kubectl apply -f k8s/deployment.yaml
```

By default it creates:

- `Namespace`: `linkwork-mcp-gateway`
- `Deployment`: 2 replicas
- `Service`: ClusterIP + NodePort (30890)

## Dependencies

- Upstream: `linkwork-server` (MCP registry source)
- Downstream: MCP servers (SSE/HTTP/stdio)
- Middle layer: Redis (cache and usage)
