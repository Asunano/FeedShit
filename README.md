# FeedShit

轻量级多项目反馈收集系统。基于 Go/Gin 构建，支持项目管理、文件上传、邮件通知、CSV 导出等功能。

## 功能特性

- **多项目管理** — 每个项目拥有独立的反馈页面和专属链接（`/fb/{slug}`）
- **文件上传** — 支持截图和日志文件上传（图片预览、日志在线查看）
- **工作量证明（PoW）** — 基于 SHA-256 的客户端计算，防止自动化垃圾提交
- **IP 限速** — 每 IP 每小时提交次数限制
- **邮件通知** — 新反馈自动邮件通知（SMTP）
- **CSV 导出** — 一键导出反馈数据，支持 Excel UTF-8 BOM
- **管理后台** — 项目 CRUD、反馈管理、配置管理（邮件/账户/系统）
- **安装向导** — 首次访问自动引导至设置页面
- **CDN 兼容** — 正确识别 Cloudflare / 代理后的真实客户端 IP
- **Docker 部署** — 多阶段构建，纯静态二进制，非 root 运行

## 快速开始

### Docker（推荐）

```bash
docker compose up -d
```

访问 `http://localhost:8080`，首次访问自动跳转至安装向导。

### 本地构建

```bash
# 需要 Go 1.26+
go build -o feedshit ./cmd/feedshit/
./feedshit
```

### 环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `PORT` | `8080` | 监听端口 |
| `DATA_DIR` | `./data` | 数据存储目录 |
| `ADMIN_USERNAME` | `admin` | 默认管理员（可通过安装向导设置） |
| `ADMIN_PASSWORD` | `changeme` | 默认密码 |
| `POW_DIFFICULTY` | `4` | PoW 前导零位数 |
| `RATE_LIMIT_PER_HOUR` | `3` | 每 IP 每小时提交上限 |
| `MAX_UPLOAD_MB` | `20` | 最大上传文件大小 (MB) |
| `SMTP_HOST` | | SMTP 服务器 |
| `SMTP_PORT` | `587` | SMTP 端口 |
| `SMTP_USER` | | SMTP 用户名 |
| `SMTP_PASS` | | SMTP 密码 |
| `SMTP_FROM` | | 发件人地址 |
| `SMTP_TO` | | 收件人地址（逗号分隔） |
| `NOTIFY_ENABLE` | `false` | 启用邮件通知 |

邮件通知也可通过管理后台的「设置 → 邮件通知」页面配置。

## 项目结构

```
├── cmd/feedshit/       # 入口
├── internal/
│   ├── app/            # HTTP handlers
│   ├── config/         # 环境变量配置
│   ├── database/       # SQLite 数据层
│   ├── email/          # SMTP 邮件发送
│   ├── middleware/      # 认证、限速、PoW
│   └── routes/         # 路由注册 + 前端 HTML
│       └── frontend/   # 嵌入式前端页面
├── test/               # E2E 测试
├── Dockerfile
└── docker-compose.yml
```

## 使用方式

1. 部署后访问首页，完成安装向导（设置管理员账号）
2. 进入管理后台 `/admin`，创建反馈项目
3. 每个项目会生成专属反馈链接 `/fb/{slug}`
4. 将链接分发给用户收集反馈

## License

MIT
