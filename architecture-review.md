# FeedShit 系统架构评估报告

> 评估人：Archi（阿奇）· 系统架构师
> 评估范围：全部 44 个 .go 源文件（排除 _test.go）
> 评估日期：2026-07-21

---

## 🏗️ 架构评估报告

### 1. 分层与职责

#### 1.1 分层结构

```
cmd/feedshit/main.go        # 入口 → 初始化链
internal/config/             # 环境变量配置
internal/security/           # AES-GCM 加密层
internal/database/           # SQLite 数据访问层（18 个文件）
internal/middleware/         # 中间件（auth, CSRF, rate-limit, session, IP）
internal/email/              # SMTP 邮件发送
internal/report/             # 周报生成
internal/app/                # HTTP 处理器（14 个文件）
internal/routes/             # 路由注册
```

#### 1.2 职责评价

| 层级 | 职责清晰度 | 评价 |
|------|-----------|------|
| `cmd/feedshit` | 🟢 清晰 | 仅做初始化编排，不包含业务逻辑 |
| `config` | 🟢 清晰 | 纯配置加载，无外部依赖 |
| `security` | 🟢 清晰 | 单一职责：AES-GCM 加解密 |
| `database` | 🟢 清晰 | 数据访问层，定义所有模型和 CRUD |
| `middleware` | 🟢 清晰 | 横切关注点分离良好 |
| `app` | 🟢 清晰 | 处理器层，依赖 database + middleware + email |
| `routes` | 🟡 需关注 | 路由注册中包含部分业务逻辑（HTML 渲染、权限校验 in Guard） |
| `email` | 🟢 清晰 | 邮件模板构建 + SMTP 发送 |
| `report` | 🟢 清晰 | 周报数据采集 + 渲染 + 发送 |

#### 1.3 循环依赖检查

- `config` → 无内部依赖
- `database` → `security`, `config`（InitDefaultConfig）
- `app` → `database`, `config`, `middleware`, `email`
- `middleware` → 无内部包依赖（自包含）
- `email` → `database`
- `report` → `database`, `email`
- `security` → 纯标准库

**结论：无循环依赖，依赖图健康。**

#### 1.4 关注点

**routes 层混合了业务逻辑**：`routes.go` 中的 `setupGuard` 内联了权限检查逻辑；`/fb/:slug` 路由处理中嵌入了项目数据获取、分类查询、HTML 渲染等逻辑，应属于 `app` 层职责。

---

### 2. 数据流分析

#### 2.1 请求处理流程

```
HTTP Request
  → Gin Engine
    → setupGuard（初始化检查）
    → Security Headers middleware
      → AuthMiddleware / CSRFMiddleware / RateLimitMiddleware
        → App Handler（验证 → 业务逻辑 → DB 调用）
          → Webhook Enqueue（异步 goroutine）
          → Email Notification（异步 goroutine）
            → JSON Response
```

#### 2.2 典型写流程（提交反馈）

```
SubmitFeedback()
  → MaxBytesReader（大小限制）
  → ParseMultipartForm
  → IsProjectActive() → DB 读
  → VerifyPoW + NonceCache 防重放
  → saveUpload() x N（文件保存）
  → InsertFeedback() → DB 写（持 Lock）
  → Mailer.SendFeedbackNotification()（goroutine）
  → SendWebhookNotification()（goroutine）
  → JSON 200
```

#### 2.3 典型读流程（列表查询）

```
AdminListFeedbacks()
  → 权限解析（role + member_grants）
  → ListFeedbacks() / SearchFeedbacks()
    → 获取 RLock
    → COUNT(*) + SELECT 分页
    → 投票数子查询
    → 释放 RLock
  → JSON
```

#### 2.4 数据流评价

🟢 **优点**：
- 读写分离明确，goroutine 用于非关键路径（邮件、webhook）
- 异步操作使用 `go func()` 而非复杂消息队列，适合轻量级场景
- 请求上下文传递清晰（`c.Set`/`c.Get`）

🟡 **问题**：
- 批量列表查询中，投票数子查询是 N+1 优化后的批量子查询，但发生在持有 RLock 期间，增加锁持有时间
- 邮件发送和 webhook 异步 goroutine 没有结构化错误处理或重试（webhook 有 outbox 表重试机制，但邮件没有）

---

### 3. API 设计评估

#### 3.1 RESTful 程度

| 维度 | 评估 |
|------|------|
| 资源命名 | 🟢 良好：`/feedbacks/:id/notes`、`/projects/:id/categories` |
| HTTP 方法 | 🟢 良好：GET/POST/PUT/DELETE/PATCH 使用正确 |
| 状态码 | 🟢 良好：200/201/400/401/403/404/409/413/429 使用恰当 |
| 版本管理 | 🟢 `/api/v1/` 路径前缀 |
| 分页 | 🟢 `?limit=&offset=` 参数 |
| 过滤 | 🟢 `?status=&project=&keyword=` 参数 |

#### 3.2 端点分析

**公开 API**（/api/v1/）：
```
GET    /health                          # 健康检查
GET    /setup                           # 安装页面
GET    /api/v1/setup/status             # 安装状态
POST   /api/v1/setup                    # 安装
GET    /api/v1/projects                 # 项目列表
GET    /api/v1/roadmap                  # 路线图
POST   /api/v1/feedback/submit          # 提交反馈
POST   /api/v1/feedback/:id/vote        # 投票
GET    /api/v1/feedback/check-duplicate # 查重
GET    /api/v1/faq                      # FAQ 搜索
GET    /api/v1/track/feedback           # 跟踪查询
POST   /api/v1/track/reply             # 提交者回复
POST   /api/v1/track/:token/rating     # 评分
POST   /api/v1/external/feedback        # API Token 提交
```

**管理 API**（/api/v1/admin/）：
```
POST   /api/v1/admin/login              # 登录
POST   /api/v1/admin/logout             # 登出
GET    /api/v1/admin/csrf-token         # CSRF token
GET    /api/v1/admin/me                 # 当前用户

# 反馈管理
GET    /api/v1/admin/feedbacks          # 列表
GET    /api/v1/admin/feedbacks/export   # 导出
GET    /api/v1/admin/feedbacks/:id      # 详情
PUT    /api/v1/admin/feedbacks/:id/status # 状态
... （RESTful 子资源模式一致）
```

#### 3.3 关注点

🟡 **API 设计**：
- `/feedbacks/export` 是动作式 URL（应为 `?format=csv` 参数化）
- `/feedback/check-duplicate` 中 `check-` 是动词前缀，建议 `?q=` 参数化
- 但以上是务实设计，开发者体验良好，无需过度纠结 REST 纯正性

🟢 整体评分：**良好**。一致性强，版本管理到位，错误信息中文化有用。

---

### 4. 数据模型评估

#### 4.1 模式设计

```
feedbacks          # 主表：22 列
  - id (PK), project_id, title, description, custom_data, file_paths
  - client_ip, status, tags, assignee
  - contact_name, contact_email, tracking_token, priority
  - is_duplicate, duplicate_of, content_hash, category
  - public_on_roadmap, roadmap_status, created_at, updated_at

feedback_notes     # 备注/回复表
feedback_ratings   # CSAT 评分
feedback_votes     # 投票表（PK: feedback_id + voter_key）
projects           # 项目表
categories         # 分类字典
faqs               # 知识库
admins             # 管理员表
member_grants      # 细粒度 RBAC
api_tokens         # API 密钥
webhook_subscriptions  # Webhook 订阅
webhook_outbox     # Webhook 投递队列
audit_logs         # 审计日志
slug_history       # Slug 重定向历史
job_locks          # 分布式作业锁
config             # 键值配置
schema_versions    # 版本迁移跟踪
```

#### 4.2 索引分析

| 表 | 索引 | 评价 |
|----|------|------|
| feedbacks | project_id, created_at, status, tracking_token, assignee, priority, (project_id, content_hash), (project_id, category) | 🟢 **充分覆盖**所有查询路径 |
| feedback_notes | feedback_id | 🟢 足够 |
| feedback_votes | feedback_id | 🟢 足够 |
| categories | project_slug | 🟢 足够 |
| faqs | project_slug, (project_slug, is_active) | 🟢 足够 |
| member_grants | admin_id | 🟢 足够 |
| webhook_outbox | next_at | 🟢 足够 |
| audit_logs | created_at | 🟢 足够 |

#### 4.3 版本化迁移

🟢 **亮点**：实现了完整的版本化迁移系统，22 个迁移步骤（v1-v22），包含 DDL 和数据迁移（BackfillContentHashes）。幂等性处理完善（`INSERT OR IGNORE`、`duplicate column` 静默忽略）。

#### 4.4 SQLite 配置

```go
PRAGMA journal_mode=WAL       // WAL 模式
PRAGMA busy_timeout=5000      // 5 秒忙等待
PRAGMA synchronous=NORMAL     // 性能与安全均衡
PRAGMA cache_size=-8000       // 8MB 缓存
PRAGMA foreign_keys=ON        // 外键约束
```

#### 4.5 关注点

🟡 **数据模型设计**：
- feedbacks 表列数较多（22 列），`SELECT *` 在 ListFeedbacks/SearchFeedbacks 中显式列出 20+ 列，可维护性一般——但 Go 的逐列 Scan 模式需要如此
- `custom_data` 和 `file_paths` 以 JSON 字符串存储，不支持 SQL 级查询——对轻量级系统可接受
- `content_hash`（SHA-256）用于精确查重，但非模糊匹配——产品约束明确

🟢 整体评价：**良好**。索引覆盖充分，迁移系统设计专业。

---

### 5. 并发模型分析

#### 5.1 核心策略

```go
type Database struct {
    db *sql.DB
    mu sync.RWMutex    // 手动串行化
}
```

- `SetMaxOpenConns(1)` + `SetMaxIdleConns(1)`：单连接
- 写操作：`mu.Lock()`（18 处）
- 读操作：`mu.RUnlock()`（25 处）
- 所有 database 方法入口获取锁，退出释放

#### 5.2 锁覆盖率

✅ **全部 18 个 database 文件中的方法都有正确的 Lock/RLock 保护**，包括：
- `InsertFeedback` → Lock
- `ListFeedbacks` → RLock
- `UpdateFeedbackStatus` → Lock
- `BackupDatabase` → Lock（VACUUM INTO）

#### 5.3 锁粒度评估

| 场景 | 锁持有时间 | 风险 |
|------|-----------|------|
| 单行 INSERT | ~1ms | 🟢 极低 |
| 分页 SELECT（ListFeedbacks） | 含 COUNT + SELECT + 投票批量子查询 | 🟡 中等——大数据量时锁持有时间增长 |
| VACUUM INTO 备份 | 数秒 | 🟠 高——全库锁 |
| CSV 导入（ImportFeedback 逐行） | 逐行 Lock/Unlock | 🟢 低 |

#### 5.4 并发模型评价

🟢 **优点**：
- 所有 database 方法有正确锁保护，无遗漏
- 读多写少的场景使用 RLock/RUnlock 区分
- 代码中显式标注了 `EnqueueWebhook` 的潜在死锁风险并正确处理

🟡 **问题与建议**：

1. **RWMutex + 单连接 = 冗余**：`SetMaxOpenConns(1)` 已经保证了 sql.DB 层面的串行访问（Go 的 `database/sql` 在单连接下内部互斥）。外层的 RWMutex 在单连接模式下没有实际并发收益，反而增加了锁开销。

2. **WAL 的读并发未利用**：代码注释明确说明"WAL is kept for crash recovery, but concurrent reads are not utilized"。当前策略主动放弃了 SQLite WAL 模式的读并发能力。

3. **VACUUM INTO 持锁**：备份期间持有 Write Lock，阻塞所有读写操作。

4. **逐行持有 RLock 的复杂查询**：`ListFeedbacks` 中的投票子查询在 RLock 下执行，反馈表数据量大时（>10 万条）会显著增加锁竞争。

**建议**：对于当前规模（轻量级），策略是务实且安全的。若需提升吞吐量，可考虑：
- 移除 `SetMaxOpenConns(1)`，利用 WAL 的并发读能力
- 保留 RWMutex 作为上层协调，允许并发读（WAL 模式下读不阻塞读）
- 备份使用文件系统快照替代 VACUUM INTO

---

### 6. 扩展性评估

#### 6.1 约束认知

项目设计者清楚 SQLite 的局限，并在其约束下做了务实的设计决策：

```
SQLite 单点限制 → 所有扩展性权衡围绕此展开
```

#### 6.2 当前架构下的扩展策略

| 维度 | 策略 | 评价 |
|------|------|------|
| 读扩展 | RWMutex + 单连接 | 🟡 不利用 WAL 并发读 |
| 写扩展 | 串行写 | 🟡 SQLite 串行写是硬约束 |
| 多实例 | job_lock 表 + 15s 轮询 | 🟢 轻量级多实例协调 |
| 数据量 | SQLite 适合 < 100 万行 | 🟢 产品定位匹配 |
| 文件存储 | 本地文件系统 | 🟡 不适合多实例部署 |
| 备份 | VACUUM INTO 备份 | 🟢 一致性保证好，但持锁 |

#### 6.3 扩展性评价

🟢 **务实且匹配产品定位**。对于轻量级反馈收集系统，SQLite 单节点是合理选择。

🟡 **未来扩展路径**：
- 若需要多实例部署，文件上传需要共享存储（NFS/S3）
- 若数据量超百万行，需考虑迁移到 PostgreSQL（数据库层抽取接口）
- 当前 database 层直接暴露 `*sql.DB` 结构体方法，没有接口抽象层，未来替换存储后端的重构成本较高
- `ExecRaw`/`QueryRaw` 方法允许上层（report 包）绕过 database 层直接执行 SQL，破坏了分层封装

---

### 7. 安全架构评估

#### 7.1 安全控制矩阵

| 安全维度 | 实现 | 评价 |
|---------|------|------|
| **存储加密** | AES-256-GCM，`aes-gcm:base64` 格式 | 🟢 强加密 |
| **密钥管理** | 环境变量 / 文件 / auto-generate 三重策略 | 🟢 灵活且安全 |
| **密码哈希** | bcrypt | 🟢 行业标准 |
| **会话管理** | 内存 session + 随机 token | 🟢 24h TTL + 自动清理 |
| **CSRF 防护** | Double-Submit Cookie 模式 | 🟢 有效 |
| **SQL 注入** | 参数化查询（全库无一例外） | 🟢 完美 |
| **XSS 防护** | `html.EscapeString` + SVG 消毒 | 🟢 全面 |
| **路径穿越** | `filepath.Clean` + `EvalSymlinks` 双重校验 | 🟢 彻底 |
| **暴力破解** | IP 级别 10 次/15分钟锁定 | 🟢 适度 |
| **频率限制** | 内存 IP 级别 + API Token 级别 | 🟢 双层 |
| **PoW 防垃** | SHA-256 工作量证明 + Nonce 缓存 | 🟢 有效 |
| **HTTPS** | `X-Forwarded-Proto`/`CF-Visitor` 检测 | 🟢 代理友好 |
| **审计日志** | 所有管理操作记录 | 🟢 完善 |
| **RBAC** | 角色（4级）+ 细粒度 grants | 🟢 设计良好 |

#### 7.2 密钥安全

```
master key 获取优先级：
1. FEEDSHIT_MASTER_KEY 环境变量（最高优先级）
2. data/key/master.key 文件（自动生成 + 仅 0400 权限）
3. 首次运行时自动生成 32 字节随机密钥
```

```go
// 密钥仅存于进程内存，永不序列化
var masterKey []byte
// Init 函数注释明确说明
```

🟢 **优点**：密钥永不离开进程内存，加密格式标准（AES-GCM with random nonce）。

🟡 **注意**：
- `EncryptFile`/`DecryptFile` 函数存在但未在运行时使用——是运维工具函数，可安全保留
- 密钥轮换策略缺失：当前不支持使用新密钥重新加密所有已存数据

#### 7.3 安全架构评价

🟢 **优秀**。作为轻量级项目，安全架构覆盖全面，为数不多在所有路径上都实现了参数化查询的项目之一。CSRF + XSS + SQL 注入 + 路径穿越 + 加密 + 限流 + 审计的完整防御链。

---

### 8. 架构债识别

#### 8.1 欠抽象区域

| 位置 | 描述 | 严重度 |
|------|------|--------|
| `database/` 无接口抽象 | 所有地方直接使用 `*database.Database`，替换存储后端需改所有调用处 | 🟡 中等 |
| `ExecRaw`/`QueryRaw` 在 `report` 包的使用 | report 包通过原始 SQL 绕过 database 层，report 中的 `GetWeeklyStats` 等方法本应由 database 层提供 | 🟡 中等 |
| `routes.go` 中的内联 HTML 和数据逻辑 | `/fb/:slug` 路由处理器含项目数据获取、JSON 序列化、HTML 替换等逻辑 | 🟢 低 |
| `config.go` AdminGetSystemConfig 混合查询 | 从 DB 读取配置和从 struct 读取配置混合在同一个处理器中 | 🟢 低 |

#### 8.2 过度设计/过早优化

| 位置 | 描述 | 评价 |
|------|------|------|
| **RWMutex 手动管理** | 在单连接模式下，Go `database/sql` 已有内部互斥，外层 RWMutex 是冗余的 | 🟢 安全性无影响，但增加代码复杂度 |
| **NonceCache 的 TTL 清理** | 10 分钟 TTL 的 goroutine 清理 + Mutex 保护，对非生产质量的 PoW 场景来说充分且必要 | 🟢 合理 |
| **member_grants 细粒度 RBAC** | 支持 `(admin, project, category) → role` 三级，对于轻量级反馈系统可能过于复杂 | 🟡 可简化，但现有设计正确 |

#### 8.3 可改进设计模式

```go
// 当前：database 包通过结构体方法导出
// 改进建议：定义 Database 接口
type FeedbackStore interface {
    InsertFeedback(f *Feedback) (int64, error)
    GetFeedback(id int64) (*Feedback, error)
    ListFeedbacks(...) ([]Feedback, int, error)
    // ...
}
```

#### 8.4 架构债评级

| 类型 | 数量 | 评级 |
|------|------|------|
| 🔴 严重 | 0 | 无阻止性问题 |
| 🟡 中等 | 3 | 接口抽象、report 绕过、RWMutex 冗余 |
| 🟢 轻微 | 4 | 路由混合逻辑、config 混查等 |

---

### 总结评级

```
整体架构健康度: 🟢 良好
```

| 评估维度 | 评级 | 要点 |
|---------|------|------|
| 1. 分层与职责 | 🟢 良好 | 职责清晰，无循环依赖，routes 层轻微职责越界 |
| 2. 数据流 | 🟢 良好 | 异步路径分离，goroutine 用法合理 |
| 3. API 设计 | 🟢 良好 | RESTful 度一致，版本管理到位 |
| 4. 数据模型 | 🟢 良好 | Schema 设计专业，索引覆盖充分，迁移系统完整 |
| 5. 并发模型 | 🟡 需关注 | RWMutex + 单连接冗余；WAL 并发读未利用 |
| 6. 扩展性 | 🟢 良好 | 务实匹配产品定位；DB 接口抽象缺失是未来隐患 |
| 7. 安全架构 | 🟢 优秀 | 防御全面，路径全覆盖，为轻量级项目示范级 |
| 8. 架构债 | 🟢 低负债 | 无严重债务，3 项中等可改进项 |

### 关键发现摘要

**最强项**：
1. **安全架构**：AES-256-GCM 静态加密 + bcrypt + XSS/SQLi/CSRF/路径穿越全防御 + 审计日志——对于轻量级项目极为出色
2. **数据库迁移系统**：版本化迁移 22 步，幂等设计，Go 版数据回填
3. **分层清晰度**：依赖图单向无循环，各包职责明确

**最需关注**：
1. **并发模型**：RWMutex 在单连接下的冗余，以及 VACUUM INTO 备份期间的写锁阻塞——当前规模无害，但需了解其限制
2. **数据库接口抽象缺失**：直接使用具体类型而非接口，未来替换存储后端的重构成本较高
3. **report 包绕过 database 层**：`ExecRaw`/`QueryRaw` 允许上层绕过封装直接执行 SQL，破坏了分层原则

**一句话评价**：
> FeedShit 是一个架构健康、安全意识极强的轻量级项目。它在 SQLite 单节点的约束下做了务实的设计选择，安全防线远超同类项目平均水平。主要架构债集中在数据库层缺乏接口抽象和并发策略的保守冗余，但不影响当前功能交付和运维可靠性。
