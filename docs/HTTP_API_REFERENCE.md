# HTTP API 接口说明

基础路径：`/api`
统一响应结构：
```json
{
  "code": 200,
  "data": {},
  "message": "..."
}
```

认证概览：
- 未配置 `server.api_key` 时，所有 `/api/*` 公开访问。
- 配置 `server.api_key` 后，除 `/api/health` 与 `/api/auth/*` 外，其他 `/api/*` 都需要 JWT。
- 认证方式：
  - `Authorization: Bearer <jwt>`
  - Cookie `vget_session=<jwt>`

---

## 1) 健康检查

### GET `/api/health`
无需认证。

响应 `data`：
```json
{
  "status": "ok",
  "version": "0.12.14"
}
```

---

## 2) 认证

### GET `/api/auth/status`
无需认证。返回是否已配置 API Key。

响应 `data`：
```json
{
  "api_key_configured": true
}
```

### POST `/api/auth/token`
无需认证。用于生成 API Token（前提是已配置 API Key）。

请求体（可选）：
```json
{
  "payload": {
    "role": "admin"
  }
}
```

响应 `data`：
```json
{
  "jwt": "<token>"
}
```

说明：
- 未配置 API Key 时，响应体 `code=500`，但 HTTP 状态仍为 200。
- Token 类型为 `api`，有效期 365 天。

---

## 3) 下载相关

### POST `/api/download`
创建下载任务或直接流式返回文件。

请求体：
```json
{
  "url": "https://example.com/video.mp4",
  "filename": "optional-name.mp4",
  "return_file": false
}
```

行为：
- `return_file=true`：直接流式返回文件。
- `return_file=false`（默认）：加入队列并返回任务 ID。

排队响应 `data`：
```json
{
  "id": "<job_id>",
  "status": "queued"
}
```

流式响应：
- 返回文件流，带 `Content-Disposition` 文件名。

### POST `/api/bulk-download`
批量下载。

请求体：
```json
{
  "urls": [
    "https://a.com/1.mp4",
    "https://b.com/2.mp4"
  ]
}
```

响应 `data`：
```json
{
  "jobs": [
    {"id": "<id>", "url": "...", "status": "queued"},
    {"id": "<id>", "url": "...", "status": "failed", "error": "..."}
  ],
  "queued": 1,
  "failed": 1
}
```

### GET `/api/status/:id`
查询单个任务状态。

响应 `data`：
```json
{
  "id": "<id>",
  "status": "downloading",
  "progress": 42.5,
  "filename": "/path/to/file.mp4",
  "error": ""
}
```

### GET `/api/jobs`
列出所有任务。

响应 `data`：
```json
{
  "jobs": [
    {
      "id": "<id>",
      "url": "...",
      "status": "completed",
      "progress": 100,
      "downloaded": 123,
      "total": 456,
      "filename": "/path/to/file.mp4",
      "error": ""
    }
  ]
}
```

### DELETE `/api/jobs`
清理已完成/失败/取消的任务。

响应 `data`：
```json
{
  "cleared": 10
}
```

### DELETE `/api/jobs/:id`
取消运行中的任务或移除已完成任务。

响应 `data`：
```json
{
  "id": "<id>"
}
```

### GET `/api/download?path=...`
下载服务器输出目录中的文件。

查询参数：
- `path`（必填）：文件路径

说明：
- 服务器会校验路径必须在输出目录内。
- 响应为文件下载流。

---

## 4) 配置

### GET `/api/config`
获取当前配置快照。

响应 `data`：
```json
{
  "output_dir": "/path",
  "language": "zh",
  "format": "mp4",
  "quality": "best",
  "twitter_auth_token": "...",
  "server_port": 8080,
  "server_max_concurrent": 10,
  "server_api_key": "..."
}
```

### POST `/api/config`
按 key 写入配置值。

请求体：
```json
{
  "key": "output_dir",
  "value": "/new/path"
}
```

支持的 key：
- `language`
- `output_dir`
- `format`
- `quality`
- `twitter_auth_token` 或 `twitter.auth_token`
- `server.max_concurrent` 或 `server_max_concurrent`
- `server.api_key` 或 `server_api_key`

### PUT `/api/config`
以结构化字段更新配置（目前仅支持 `output_dir`）。

请求体：
```json
{
  "output_dir": "/new/path"
}
```

---

## 5) 国际化

### GET `/api/i18n`
返回当前语言的 UI 文案。

响应 `data`：
```json
{
  "language": "zh",
  "ui": {"download": "..."},
  "server": {"no_config_warning": "..."},
  "config_exists": true
}
```

---

## 6) 状态码与错误

JSON 包装体 `code` 常见值：
- `200`: 成功
- `201`: 创建成功（例如生成 token）
- `400`: 请求参数错误
- `401`: 未授权（启用 API Key 后）
- `403`: 禁止访问（路径不在输出目录）
- `404`: 资源不存在
- `500`: 服务器错误

HTTP 状态码通常与 `code` 一致，例外：
- `/api/auth/token` 即便失败也返回 HTTP 200，实际错误在 `code` 字段中。

---

## 7) 任务状态枚举

- `queued`
- `downloading`
- `completed`
- `failed`
- `cancelled`

