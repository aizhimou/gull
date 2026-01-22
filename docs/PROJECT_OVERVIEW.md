# vget 项目说明（面向 Java 工程师）

本文档基于仓库现有源码整理，帮助你在不熟悉 Go 的情况下，理解项目的功能、结构、运行方式与维护要点。

## 1. 项目定位与目标

vget 是一个**媒体下载服务**，以 HTTP API 的形式提供下载能力。核心能力包括：
- 解析不同站点的媒体链接（extractor），得到实际下载地址
- 支持队列化下载、任务状态查询、批量下载
- 支持 HLS（m3u8）分片下载、合并、进度回报
- 可选使用浏览器自动化提取媒体流（适配复杂网站）
- 支持基础的 API Key 认证（JWT）

仓库中目前只包含 **vget-server**（HTTP 服务），命令行 UI 与前端 UI 在 Makefile 里被引用，但目录/代码不在当前仓库中（需要注意）。

## 2. 功能列表（按模块）

### 2.1 HTTP 服务（`internal/server`）
- 健康检查：`GET /api/health`
- 下载任务：
  - `POST /api/download`：创建下载任务（或直接流式返回文件）
  - `POST /api/bulk-download`：批量下载
  - `GET /api/status/:id`：查询任务状态
  - `GET /api/jobs`：列出全部任务
  - `DELETE /api/jobs`：清理历史任务
  - `DELETE /api/jobs/:id`：取消/删除任务
- 直接下载已保存文件：`GET /api/download?path=...`
- 配置管理：
  - `GET /api/config`：查看当前配置
  - `POST /api/config`：按 key 设置配置
  - `PUT /api/config`：更新 output_dir
- 国际化文本：`GET /api/i18n`
- 认证：
  - `GET /api/auth/status`
  - `POST /api/auth/token`：生成 API Token

### 2.2 下载队列（`internal/server/job.go`）
- 内存队列 + 固定 worker 池并发下载
- 任务状态：queued/downloading/completed/failed/cancelled
- 定期清理 1 小时前完成/失败任务

### 2.3 解析器 Extractor（`internal/core/extractor`）
已实现：
- Twitter/X（支持匿名、Guest Token、Auth Token）
- Apple Podcasts（iTunes API）
- 小宇宙（xiaoyuzhoufm）
- 直链文件（mp4/mp3/jpg 等）
- m3u8 直链
- 浏览器自动化（Rod + Chrome/Chromium）

待实现/占位：
- TikTok（目前返回“coming soon”）
- Instagram（目前返回“coming soon”）

### 2.4 下载器 Downloader（`internal/core/downloader`）
- 常规 HTTP 下载（支持进度）
- HLS 分片下载 + 解密（如有 Key）
- 多线程分块下载（Range 请求）
- 嵌入式 ffmpeg（WASM）将 `.ts` 转为 `.mp4`
- 可选调用系统 `ffmpeg` 合并视频/音频流

### 2.5 配置与国际化（`internal/core/config`, `internal/core/i18n`）
- 配置文件：`~/.config/vget/config.yml`
- 站点配置：`sites.yml`（当前工作目录）
- 多语言 UI 文案（内置 yml）

### 2.6 其他
- 版本信息：`internal/core/version/version.go`
- 简单加密工具（PIN + AES-GCM）：`internal/core/crypto`

## 3. 目录结构说明

```
.
├── main.go                   # HTTP 服务入口
├── internal/
│   ├── server/               # API、认证、中间件、下载队列
│   └── core/
│       ├── config/           # 配置管理、sites.yml
│       ├── downloader/       # 下载实现
│       ├── extractor/        # 站点解析器
│       ├── i18n/             # 国际化资源
│       ├── crypto/           # 加密工具
│       └── version/          # 版本信息
├── Dockerfile                # 构建 vget-server 镜像
├── docker-compose.yml        # 示例编排
├── go.mod / go.sum           # Go 依赖
└── Makefile                  # 构建脚本（包含 UI / CLI 但不在本仓库）
```

## 4. 核心架构与处理流程

### 4.1 下载流程（简化）
1. 客户端调用 `POST /api/download`
2. 服务器 `JobQueue` 创建任务并进入队列
3. Worker 选择合适的 `Extractor`：
   - 先按 host 匹配内置解析器
   - 如果没有，尝试 `sites.yml` 配置的浏览器提取
   - 最后回退到通用浏览器提取或直链
4. Extractor 返回 `Media`（视频/音频/图片）
5. 根据类型选择下载策略：
   - 视频/音频：普通下载或 HLS 下载
   - 图片：逐张下载
   - 自适应视频流（含 AudioURL）：双流下载后用 ffmpeg 合并
6. 更新任务进度/状态

### 4.2 认证模型
- 如果 `server.api_key` 配置了：
  - `/api/health` 和 `/api/auth/*` 免认证
  - 其他 `/api/*` 需要 session cookie 或 Bearer JWT
- Token 由 `/api/auth/token` 生成

## 5. 配置文件与关键参数

### 5.1 全局配置（`~/.config/vget/config.yml`）
常用字段：
- `language`: 语言（默认 `zh`）
- `output_dir`: 下载输出目录
- `format`: 偏好格式（mp4/webm/mkv/best）
- `quality`: 质量（best/1080p/720p...）
- `twitter.auth_token`: X 的 auth_token（用于 NSFW/私密内容）
- `server.port`: 服务端口
- `server.max_concurrent`: 最大并发下载
- `server.api_key`: API Key（用于 JWT）

### 5.2 站点配置（`sites.yml`）
用于浏览器提取：
- `match`: URL 包含的域名或关键字
- `type`: 期望提取的媒体类型（目前常用 m3u8/mp4）

## 6. 运行依赖与环境要求

### 构建依赖
- Go 1.25（`go.mod` 指定）

### 运行时依赖
- **基本运行**：仅需可执行文件 + 网络
- **可选增强**：
  - `ffmpeg`：用于合并分离的视频/音频流
  - Chrome/Chromium：用于 BrowserExtractor（复杂网站解析）

### Docker 相关
- Dockerfile 未安装浏览器，如果需要浏览器提取，镜像内需额外安装 Chromium 并设置 `ROD_BROWSER`。

## 7. 部署方式

### 7.1 本地运行
```bash
# 构建
go build -o vget-server .

# 运行
./vget-server --port 8080 --output ~/Downloads/vget
```

### 7.2 Docker
```bash
# 构建镜像
docker build -t vget-server .

# 运行
docker run -p 8080:8080 -v $(pwd)/downloads:/downloads vget-server
```

### 7.3 docker-compose
`docker-compose.yml` 示例：
- 映射 `./downloads` 到容器 `/downloads`
- 映射 `./config` 到 `/root/.config/vget`

## 8. 维护与扩展建议（给 Java 工程师）

### 8.1 如何新增站点支持
- 在 `internal/core/extractor` 新增一个 Extractor
- 实现 `Match` + `Extract`
- 在 `init()` 中 `Register(...)`
- 复用现有 `Media` 类型即可

### 8.2 如何修改下载逻辑
- `internal/server/server.go`：下载流程入口
- `internal/core/downloader`：具体下载实现
- HLS 相关在 `hls.go`，分段、解密、转码逻辑集中在这里

### 8.3 API 扩展
- 路由定义在 `internal/server/server.go`
- 新增 handler 后加入 `api.Group` 即可

## 9. 已知“代码存在但未完全接入”部分
- Makefile 里引用了 `cmd/vget`（CLI）和 `ui/`，但当前仓库没有这些目录
- TikTok / Instagram extractor 只提供占位实现

---

如果你需要，我可以继续：
- 根据你想支持的站点，设计新的 Extractor 结构
- 根据你希望的部署环境，给出更详细的部署脚本（例如 systemd、k8s）
- 输出一份面向运维的健康检查和监控建议
