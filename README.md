# FeedShit

<p align="center">
  <img src="https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go&logoColor=white" alt="Go">
  <img src="https://img.shields.io/badge/License-MIT-green.svg" alt="License">
  <img src="https://img.shields.io/badge/Docker-ready-blue?logo=docker&logoColor=white" alt="Docker">
  <img src="https://img.shields.io/badge/platform-windows%20%7C%20linux%20%7C%20macos-lightgrey" alt="Platform">
</p>

> 轻量级多项目反馈收集系统 · [GitHub 项目地址](https://github.com/Asunano/FeedShit)

FeedShit 是一个用 Go 编写的**单二进制、零外部依赖**的多项目用户反馈收集系统。它提供面向公众的反馈提交页、完整的多管理员后台（含细粒度 RBAC）、外部系统 API 接入、Webhook 通知、邮件通知与提交者自助追踪，所有前端页面以原生 HTML/CSS/JS 形式编译进二进制，无需额外构建步骤。

## 快速开始

```bash
# 1. 下载对应平台的二进制，或自行编译
# 2. 运行（首次自动引导安装）
./feedshit

# 3. 打开浏览器访问 http://localhost:8080 完成安装向导
# 4. 创建项目 → 分发 /fb/{slug} 链接给用户 → 开始收集反馈
```

> **首次运行必须设置环境变量** `FEEDSHIT_MASTER_KEY`（32 字节 AES-GCM 主密钥），否则启动失败。生成方式见下方部署说明。

## 功能特性

**收集与提交**
- **多项目管理** — 每个项目拥有独立的反馈页与专属链接 `/fb/{slug}`，支持自定义 `form_schema` 与分类字典
- **公开反馈页** — 自适应表单，支持图片/日志上传、分类选择、自定义字段
- **工作量证明（PoW）** — 基于 SHA-256 前导零的客户端计算，防自动化垃圾提交；带 nonce 重放防护
- **IP 限速** — 每 IP 每小时提交次数限制（默认 10，Docker 镜像覆盖为 3）
- **重复检测** — 提交时自动计算内容指纹（归一化 SHA-256），实时提示相似反馈防止重复提交
- **FAQ 知识库** — 提交页面内联 FAQ 自助查询，按项目隔离、支持停用管理
- **提交者自助追踪** — 通过跟踪令牌在 `/track` 查看处理状态并可追加回复
- **文件上传安全** — 扩展名白名单 + 文件魔数（magic bytes）校验，SVG 自动清洗 XSS

**管理与协作**
- **多管理员团队** — `admins` 表存储账号，角色分 `admin / manager / editor / viewer`
- **细粒度 RBAC** — `member_grants`（管理员 × 项目 × 分类 → 角色）实现项目/分类级数据隔离
- **反馈全生命周期** — 状态（pending/processing/resolved/closed）、优先级（low/medium/high/urgent）、标签、指派、重复标记、内部备注与公开回复
- **批量操作** — 批量删除/改状态/改标签/改指派/改优先级/改分类
- **暗黑模式** — 管理端支持亮/暗主题切换，自动跟随系统偏好
- **键盘快捷键** — `j/k` 导航列表、`e` 编辑、`Enter` 查看、`/` 搜索、`Esc` 关闭
- **响应式布局** — 管理端适配移动端窄屏
- **审计日志** — 记录登录、增删改、导入导出等关键操作
- **仪表盘与图表** — 总量/今日/项目统计、每日趋势、状态分布、分类分布

**集成与通知**
- **邮件通知（SMTP）** — 新反馈通知管理员、状态变更/公开回复通知提交者；支持自定义邮件模板（占位符 `{{project}}` `{{title}}` `{{description}}` `{{status}}` `{{admin_url}}` 等，用户内容做 HTML 转义）
- **Webhook 通知** — 自动适配飞书 / 钉钉 / 企业微信 / Slack / 通用 JSON，事件含新反馈、状态变更、备注回复、优先级变更、指派变更
- **外部系统 API Token** — 通过 `Bearer` 令牌（`/api/v1/external/feedback`）接受 CI、监控等系统的程序化提交
- **每周周报邮件** — 每周一 08:00 自动发送 HTML 周报邮件（分类分布、新增/解决数、每日趋势、各项目概况），多实例安全去重
- **CSV 导入/导出** — 导出带 Excel UTF-8 BOM；导入兼容中/英表头

**运维**
- **自动备份** — 启动即备份，每日 03:00 定时 `VACUUM INTO` 备份；支持手动备份与按天清理旧备份（默认保留 30 天）
- **数据归档** — 手动或按天数自动将长期未处理的反馈置为 closed
- **Slug 历史重定向** — 项目 slug 改名后旧链接自动 301 跳转
- **健康检查** — `/health` 校验数据库连通性，便于容器探针
- **CDN/代理兼容** — 可配置可信代理与 CDN 厂商（auto / cloudflare / generic / none）以准确获取真实客户端 IP
- **密钥加密** — SMTP 密码、Webhook secret 以 AES-GCM 加密落库，主密钥仅来自环境变量

## 架构概览

```mermaid
flowchart LR
  U[公众用户 / 外部系统] -->|HTTP| R[Router + Setup Guard]
  R --> MW[Middleware<br/>Auth · CSRF · PoW · RateLimit · BruteForce]
  MW --> A[App Handlers<br/>业务处理]
  A --> DB[(SQLite<br/>modernc.org/sqlite)]
  A --> EM[Mailer<br/>SMTP]
  A --> RP[Report<br/>周报推送]
  A --> WH[Webhook<br/>飞书/钉钉/企微/Slack]
  A --> FS[文件存储<br/>./data/uploads]
```

**分层结构**

| 层 | 包 | 职责 |
|----|----|------|
| 入口 | `cmd/feedshit` | 加载配置、初始化 DB 与组件、启动定时任务（备份/周报/Webhook 出站）、优雅停机 |
| 配置 | `internal/config` | 从环境变量读取运行配置 |
| 数据层 | `internal/database` | SQLite 访问（手动 RWMutex 串行化）、Schema 迁移、CRUD、RBAC 授权、备份/导入导出 |
| 业务层 | `internal/app` | 全部 HTTP 处理器（提交、后台、项目、分类、备注、RBAC、Webhook、邮件模板、CSV、归档、FAQ、去重） |
| 中间件 | `internal/middleware` | 会话认证、角色鉴权、CSRF、PoW 校验、限速、登录暴力破解防护、nonce 重放缓存、CDN IP 识别 |
| 邮件 | `internal/email` | SMTP 通知与自定义模板渲染 |
| 周报 | `internal/report` | 每周统计采集、HTML 邮件模板渲染、分布式抢锁（job_locks） |
| 路由 | `internal/routes` | 路由注册、安装前置守卫、内嵌前端 HTML |

**请求流程**
1. 所有请求先经 `setupGuard`：未完成初始设置时仅放行 `/health`、`/setup`、`/api/v1/setup/*`、`/fb/`、`/track`、`/api/v1/track/*`，其余 API 返回 `503`、页面跳转 `/setup`
2. 公众提交经限速 + PoW + nonce 校验后入库，并异步触发邮件/Webhook
3. 管理 API 经会话认证 + CSRF（写操作）+ 角色中间件后由 handler 处理；非 admin 角色按 `member_grants` 做项目/分类级数据隔离
4. 后台定时器：每日备份（03:00）、Webhook 出站轮询（每 30 秒）、每周周报邮件（周一 08:00）

> 页面（`index` / `setup` / `login` / `feedback` / `track` / `admin`）在编译期通过 `go:embed` 打包进二进制，`/fb/{slug}` 页面在请求时把项目信息动态注入。后台 `admin.html` 为带 hash 路由的单页应用，无独立构建步骤。

## 目录结构

```
├── cmd/feedshit/            # 程序入口
├── internal/
│   ├── app/                 # HTTP 处理器（业务逻辑主体）
│   ├── config/              # 环境变量配置
│   ├── database/            # SQLite 数据层（modernc.org/sqlite）
│   ├── email/               # SMTP 邮件发送与模板
│   ├── middleware/          # 认证 / CSRF / PoW / 限速 / RBAC
│   ├── report/              # 每周周报邮件（统计 + 模板 + 分布式锁）
│   └── routes/              # 路由注册 + 前端 HTML
│       └── frontend/        # 内嵌前端页面
├── test/                    # 本地测试工具（不入库）
├── deliverables/            # 设计文档（不入库）
├── Dockerfile               # 多阶段 alpine 构建，CGO_ENABLED=0
└── docker-compose.yml
```

## 数据模型

SQLite 数据库位于 `./data/feedbacks.db`（WAL 模式，单连接 + 手动 RWMutex 串行化）。

| 表 | 说明 |
|----|------|
| `feedbacks` | 反馈主表：标题、描述、`custom_data`(JSON)、`file_paths`(JSON)、内容指纹(content_hash)、状态、标签、指派、优先级、联系人、跟踪令牌、重复标记、分类、时间戳 |
| `projects` | 反馈项目：`slug`、`name`、`description`、`is_active`、`is_archived`、`form_schema` |
| `categories` | 项目级分类字典（key / name / color / sort_order / is_active） |
| `admins` | 管理员账号（bcrypt 密码哈希、角色、启用状态） |
| `member_grants` | 细粒度授权：`admin_id × project_slug × category_key → role` |
| `feedback_notes` | 反馈备注/回复（`is_public` 区分内部备注与公开回复） |
| `faqs` | FAQ 知识库条目（project_slug 隔离、question/answer、is_active、sort_order） |
| `api_tokens` | 外部系统接入令牌（`fs_` 前缀，可按项目限定） |
| `config` | 键值配置（邮件、系统、Webhook、CDN 等） |
| `audit_logs` | 操作审计日志 |
| `slug_history` | 项目 slug 改名后的重定向历史 |
| `job_locks` | 分布式作业锁（key/token/locked_until，用于周报多实例去重） |

## API 参考

基础前缀：`/api/v1`。除登录与公开接口外，管理接口需在 Cookie `admin_session` 中携带有效会话，且写操作需 `X-CSRF-Token` 头。

### 公开接口
| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/health` | 健康检查（含 DB 连通性） |
| GET | `/api/v1/setup/status` | 安装状态与 PoW 难度 |
| POST | `/api/v1/setup` | 完成初始设置（创建管理员） |
| GET | `/api/v1/projects` | 列出启用且未归档的项目 |
| POST | `/api/v1/feedback/submit` | 公众提交反馈（限速 + PoW + nonce） |
| GET | `/api/v1/feedback/check-duplicate?q=&project=` | 提交前检测相似反馈（SHA-256 内容指纹） |
| GET | `/api/v1/faq?q=&project=` | FAQ 知识库实时查询（限速） |
| GET | `/api/v1/track/feedback?token=` | 提交者按令牌查询反馈状态与公开备注 |
| POST | `/api/v1/track/reply` | 提交者追加回复（限速） |
| POST | `/api/v1/external/feedback` | 外部系统经 `Bearer` API Token 提交 |

### 管理接口（认证 + CSRF）
| 分组 | 路径（前缀 `/api/v1/admin`） | 角色要求 |
|------|------|------|
| 会话 | `/login`、`/logout`、`/csrf-token`、`/me` | 登录公开，其余已登录 |
| 仪表盘 | `/stats`、`/project-stats`、`/chart-data` | 已登录 |
| 反馈 | `/feedbacks`、`/feedbacks/export`(CSV)、`/feedbacks/:id` 及 `status`/`assignee`/`priority`/`category` 修改、`/feedbacks/:id/notes`、`/feedbacks/bulk-*`、`/feedbacks/:id/similar`(候选相似)、`/feedbacks/:id/duplicate`(标记合并) | 写操作需 editor+ |
| 项目 | `/projects`(GET/POST/PUT)、`/projects/:id/archive`、删除 | POST/PUT 需 editor+ |
| 分类 | `/projects/:id/categories`、`/categories/:id` | 增删改需 editor+ |
| FAQ 知识库 | `/projects/:slug/faqs`(GET/POST/PUT/DELETE) | editor+ |
| 团队 | `/admins`、`/admins/:id`、`/admins/:id/grants*` | admin |
| API Token | `/api-tokens`、`/api-tokens/:id` | admin |
| 数据 | `/import/csv`(editor+)、`/archive`、`/prune-backups`、`/backup`、`/audit-logs` | 归档/备份需 admin |
| 配置 | `/config/email`、`/config/status-notification`、`/config/account`、`/config/system`(含 SMTP/Webhook/CDN)、`/config/email-template` | admin |

> 角色层级：`admin(4) > manager(3) > editor(2) > viewer(1)`。非 admin 用户受 `member_grants` 约束。

## 配置

配置来源优先级：**运行时数据库 `config` 表 > 环境变量 > 代码默认值**。

| 环境变量 | 代码默认 | 说明 |
|----------|----------|------|
| `PORT` | `8080` | 监听端口 |
| `DATA_DIR` | `./data` | 数据目录（DB / 上传 / 备份均在其下） |
| `ADMIN_USERNAME` | `admin` | 默认管理员（安装向导可覆盖并写入 DB） |
| `ADMIN_PASSWORD` | `changeme` | 默认密码（安装向导设置后改为 bcrypt 哈希） |
| `BASE_URL` | `http://localhost:8080` | 邮件/链接中的基础地址 |
| `POW_DIFFICULTY` | `4` | PoW 前导零位数（1–10） |
| `RATE_LIMIT_PER_HOUR` | `10` | 每 IP 每小时提交上限（Docker 镜像覆盖为 `3`） |
| `MAX_UPLOAD_MB` | `20` | 单请求最大上传体积 (MB) |
| `SMTP_HOST` / `SMTP_PORT` | `""` / `587` | SMTP 服务器与端口 |
| `SMTP_USER` / `SMTP_PASS` | `""` | SMTP 凭据 |
| `SMTP_FROM` / `SMTP_TO` | `""` | 发件人 / 收件人（逗号分隔） |
| `NOTIFY_ENABLE` | `false` | 是否启用邮件通知 |
| `WEBHOOK_URL` | `""` | ⚠️ 已废弃：全局 webhook_url 不再触发出站通知，请改用后台「Webhook 订阅」 |
| `TRUSTED_PROXIES` | `""` | 可信代理 IP（逗号分隔，`*` 表示全部） |
| `FEEDSHIT_MASTER_KEY` | **（必填）** | AES-GCM 主密钥（32 字节原始值，或 64 位十六进制），缺失即启动失败 |
| `API_TOKEN_DEFAULT_RATE_LIMIT` | `60` | 新建外部 API Token 的默认每小时速率上限（0 = 不限） |
| `BACKUP_RETENTION_DAYS` | `30` | 每日备份保留天数，超过则自动清理 |

以上邮件/系统/Webhook/CDN 等多数设置也可在后台「系统设置」页配置并持久化到 DB。

## 部署

### 跨平台二进制下载

从 [Releases](https://github.com/Asunano/FeedShit/releases) 下载对应平台的二进制，或自行编译：

```bash
# Linux / macOS
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o feedshit ./cmd/feedshit/
GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o feedshit-macos ./cmd/feedshit/

# Windows
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o feedshit.exe ./cmd/feedshit/
```

二进制为 CGO_ENABLED=0 纯静态编译，无任何运行时依赖，可直接拷贝到目标服务器运行。

### Linux 部署

```bash
# 下载或编译 Linux 二进制
./feedshit

# 或作为 systemd 服务（需自行编写 service 单元）
# 务必设置 FEEDSHIT_MASTER_KEY 环境变量
```

运行前设置环境变量：

```bash
export FEEDSHIT_MASTER_KEY=$(openssl rand -hex 32)
export ADMIN_PASSWORD="your-strong-password"
export PORT=8080
export BASE_URL=https://feedback.example.com
./feedshit
```

### Windows 部署

```cmd
:: 设置环境变量后运行
set FEEDSHIT_MASTER_KEY=your-32-byte-hex-key
set ADMIN_PASSWORD=your-strong-password
feedshit.exe

:: 或通过 PowerShell
$env:FEEDSHIT_MASTER_KEY="your-32-byte-hex-key"
$env:ADMIN_PASSWORD="your-strong-password"
feedshit.exe
```

建议通过 NSSM 或 Windows 任务计划程序注册为后台服务。

### macOS 部署

```bash
# 直接运行
export FEEDSHIT_MASTER_KEY=$(openssl rand -hex 32)
export ADMIN_PASSWORD="your-strong-password"
./feedshit-macos
```

或通过 `launchd` 注册为开机自启服务。

### Docker（推荐生产环境）

> 部署前请复制 [`.env.example`](.env.example) 为 `.env` 填入真实配置（尤其是 `FEEDSHIT_MASTER_KEY`）。

```bash
cp .env.example .env   # 编辑 .env
docker compose up -d
```

访问 `http://localhost:8080`，首次访问自动跳转安装向导。

**重要**：
- 禁止 `scale > 1`（SQLite 单连接串行化，不支持水平扩展）
- 使用不可变版本 tag，不在 compose 中使用 `latest`
- `FEEDSHIT_MASTER_KEY` 必须设置，缺失即启动失败
- 详细运维说明见 [`docs/runbook.md`](docs/runbook.md)

## 使用流程

1. 部署后访问首页，完成安装向导（设置管理员账号与密码）
2. 进入后台 `/admin`，创建反馈项目并配置分类字典
3. 每个项目生成专属反馈链接 `/fb/{slug}`，分发给用户收集反馈
4. 新反馈触发邮件/Webhook 通知；可在后台分配优先级、指派处理人、添加备注、标记重复
5. 提交者凭跟踪令牌在 `/track` 查看进度并回复
6. 处理完成后更新状态；可定期归档旧数据、导出 CSV 或手动备份
7. 可选：在后台配置 FAQ 知识库条目，提交页面自动展示相关内容
8. 可选：在「系统设置」中配置 `report_recipients` 收件人，每周一自动接收周报邮件

## 安全说明

- 管理员密码使用 bcrypt 哈希；登录有暴力破解锁定防护（IP 级）
- 管理写操作强制 CSRF 双提交 Cookie 校验
- 公众提交需通过 PoW 与 nonce 重放校验，并受每 IP 限速约束
- 文件上传经扩展名白名单与魔数校验，SVG 内容自动剥离 XSS
- 敏感凭据（SMTP 密码、Webhook secret）以 AES-GCM 加密落库，主密钥仅来自环境变量
- 外部 API Token 新建时套用默认每小时限速，防止滥用
- 每日备份后按保留天数自动清理旧备份，避免磁盘无限增长

## License

MIT

## 项目目录结构

| 目录 | 用途 |
|------|------|
| `cmd/feedshit/` | 应用入口 + 初始化编排 |
| `internal/` | Go 内部包（config / database / middleware / app / email / report / routes / security） |
| `docs/` | **开发者文档** — ADR（架构决策记录）、功能说明（如邀请系统） |
| `deliverables/` | **工程保障输出** — 代码审查报告、事故复盘、技术债评估等（由 AI 工程保障团队生成） |
| `test/` | 测试数据（SQLite 数据库快照等） |
| `.github/workflows/` | CI/CD 配置
