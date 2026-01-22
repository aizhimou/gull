# HTTP API 认证说明（API Key 与 JWT）

本项目的 HTTP API 采用**可选的 API Key 认证**，如果没有配置 API Key，则所有 `/api/*` 接口默认允许访问；配置了 API Key 后，除健康检查与认证接口外，其余 API 都需要有效的 JWT 才能访问。

本文档重点说明认证机制、配置方式与调用示例。

## 1. 认证开关

- 配置项：`server.api_key`
- 配置位置：`~/.config/vget/config.yml`
- 行为：
  - 未配置 `server.api_key`：所有 API 无需认证
  - 配置了 `server.api_key`：除 `/api/health` 与 `/api/auth/*` 外，其他 `/api/*` 都需要 JWT

## 2. 认证方式概览

系统采用 JWT（HS256）作为 API 鉴权手段，签名密钥即 `server.api_key`。
JWT 有两类：
- **Session Token**：短期（24 小时），用于浏览器场景，通过 Cookie 传递
- **API Token**：长期（1 年），用于服务端/脚本场景，通过 `Authorization: Bearer` 传递

## 3. 相关接口

### 3.1 查看是否开启认证
- `GET /api/auth/status`
- 返回字段 `api_key_configured` 为 true/false

### 3.2 生成 API Token
- `POST /api/auth/token`
- 请求体（可选）：
  ```json
  {
    "payload": {
      "key": "value"
    }
  }
  ```
- 返回示例：
  ```json
  {
    "code": 201,
    "data": { "jwt": "<token>" },
    "message": "JWT Token generated"
  }
  ```

说明：
- 只有在配置了 `server.api_key` 后才会生成成功
- 请求体 `payload` 会写入 JWT 的自定义字段（Custom）

## 4. 认证传递方式

### 4.1 Bearer Token（推荐给服务端/脚本）
```
Authorization: Bearer <jwt>
```

### 4.2 Session Cookie（适合浏览器）
- Cookie 名称：`vget_session`
- 服务器会在 `/api/auth/*` 访问时设置 Session Cookie（如果已配置 API Key）

注意：Session Cookie 和 Bearer Token 二选一即可，二者都会被接受。

## 5. JWT 结构说明

JWT 的 claims 包含：
- `type`: "session" 或 "api"
- `exp`, `iat`, `nbf`, `iss`
- `custom`: 自定义 payload（可选）

签名算法：`HS256`

## 6. 配置方式

### 6.1 修改配置文件
编辑 `~/.config/vget/config.yml`：
```yaml
server:
  api_key: "your-secret-key"
```

### 6.2 使用 API 修改配置
```
POST /api/config
Content-Type: application/json

{
  "key": "server.api_key",
  "value": "your-secret-key"
}
```

## 7. 调用示例

### 7.1 获取 Token
```bash
curl -X POST http://localhost:8080/api/auth/token
```

### 7.2 使用 Token 调用 API
```bash
curl -X POST http://localhost:8080/api/download \
  -H "Authorization: Bearer <jwt>" \
  -H "Content-Type: application/json" \
  -d '{"url": "https://example.com/video.mp4"}'
```

## 8. 重要注意点

- 如果 `server.api_key` 为空：JWT 校验不启用
- `/api/health` 与 `/api/auth/*` 永远不需要认证
- Token 有效期：
  - Session：24 小时
  - API：365 天
- Token 由服务器生成，客户端无需自签

---

如需我补充：
- 完整 API 列表的 curl 示例（含认证）
- 结合 nginx 的反向代理配置方式
- 多环境密钥管理建议
