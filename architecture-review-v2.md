# FeedShit 系统架构二次评估报告

> 评估人：Archi（阿奇）· 系统架构师
> 评估范围：全部 .go 源文件（排除 _test.go）
> 评估日期：2026-07-22
> 本次性质：二次审查 —— 对比上一轮评估的架构债改善情况 + 新功能评估

---

## 一、四项架构债追踪

### ▎债项 1：数据库层无接口抽象

| 维度 | 上一轮评估 | 本次评估 | 变化 |
|------|-----------|---------|------|
| 状态 | 🟡 中等债务 | 🔴 未改善 | — |

**现状**：
- `App` 结构体仍持有 `DB *database.Database` 具体类型（app.go:22）
- 所有 handler、report 包、routes 包均直接调用 `a.DB.Xxx()` 具体方法
- 未引入任何接口抽象（如 `FeedbackStore`、`DBInterface`）
- 全部 18 个 database 包文件依然通过结构体方法导出

**影响**：
- 若未来需要替换存储后端（SQLite → PostgreSQL），需要修改所有调用处的签名
- 单元测试无法 mock 数据库层，所有测试必须依赖真实的 SQLite 内存库
- 当前规模下可接受，但接口缺失是唯一的**系统性重构障碍**

**建议方案**（成本 ~1 天）：

```go
// 在 database/database.go 中定义核心接口
type FeedbackStore interface {
    InsertFeedback(f *Feedback) (int64, error)
    GetFeedback(id int64) (*Feedback, error)
    ListFeedbacks(...) ([]Feedback, int, error)
    SearchFeedbacks(...) ([]Feedback, int, error)
    UpdateFeedbackStatus(id int64, status, tags string) error
    DeleteFeedback(id int64) error
    // ... 按需扩展
}
// App 结构体改为持有接口
type App struct {
    DB database.FeedbackStore
    // ...
}
```

---

### ▎债项 2：report 包绕过 database 层（ExecRaw/QueryRaw）

| 维度 | 上一轮评估 | 本次评估 | 变化 |
|------|-----------|---------|------|
| 状态 | 🟡 中等债务 | 🟡 部分改善 | ✅ 主流程已修复 |

**改善点**：
- `report/report.go` 中的 `collectWeeklyStats` 不再使用原始 SQL
- 改为调用规范的 database 方法：`GetWeeklyStats()`、`GetWeeklyStatusDistribution()`、`GetWeeklyCategoryCounts()`、`GetDailyTrendInRange()`、`GetWeeklyProjectStats()`
- 这些方法均已正确定义在 `database/stats.go` 中

**未改善点**：
- `report/joblock.go` 仍通过 `db.ExecRaw()` 执行原始 SQL（joblock.go:36, 55, 80）
- `database/config.go` 中仍保留 `ExecRaw`（line 130）和 `QueryRaw`（line 138）方法
- 测试文件 `report_test.go` 和 `joblock_test.go` 仍使用 `db.ExecRaw()`/`db.QueryRaw()` 进行测试数据准备

**当前 bypass 路径**：

```
report/joblock.go
  → db.ExecRaw("UPDATE job_locks SET ...")     ← 绕过 database 层
  → db.ExecRaw("INSERT OR IGNORE INTO ...")    ← 绕过 database 层
```

**评估**：主数据流已修复，但 job lock 仍绕行。`ExecRaw`/`QueryRaw` 维持在 `database/config.go` 中作为基础设施暴露，文档注释标明"供 report 包内部使用"。如果 job lock 逻辑也封装为 `database` 包方法，可完全消除此债务。建议后续将 job lock 操作也封装为 `database.SetJobLock()` 方法。

---

### ▎债项 3：RWMutex + 单连接冗余

| 维度 | 上一轮评估 | 本次评估 | 变化 |
|------|-----------|---------|------|
| 状态 | 🟡 中等债务 | 🔴 未改善 | — |

**现状**：
- `Database.mu sync.RWMutex` 仍在（database.go:142）
- `SetMaxOpenConns(1)` + `SetMaxIdleConns(1)` 仍在（database.go:228-229）
- 全部 27 个数据库方法仍无一例外地获取 `d.mu.RLock()` 或 `d.mu.Lock()`
- **代码注释明确承认了冗余**（database.go:226-227）：
  > "Single-connection mode: consistent with the manual RWMutex... WAL is kept for crash recovery, but concurrent reads are not utilized."

**冗余的实际成本**：
- 代码噪音：每增加一个 DB 方法，开发者必须记住加锁/解锁
- 无运行时开销（因为单连接下 sql.DB 本身串行化）
- 但 RWMutex 在 `(*sql.Rows)` 持有期被释放的问题（`QueryRaw` 场景）可能引入微妙的并发问题

**建议**：保持现状可接受。如果未来重构，可以考虑：
- 方案 A：移除 RWMutex，完全依赖 sql.DB 的内置连接池互斥（需将 `SetMaxOpenConns` 提升到 >1 以利用 WAL 并发读）
- 方案 B：保持 RWMutex 但对 SQLite 连接设置 `_pragma=busy_timeout` 并移除 `SetMaxOpenConns(1)`，利用 SQLite WAL 模式的读写不互斥特性

---

### ▎债项 4：routes 层混合业务逻辑

| 维度 | 上一轮评估 | 本次评估 | 变化 |
|------|-----------|---------|------|
| 状态 | 🟢 轻微债务 | 🟡 未改善 | — |

**现状**：
- `routes/routes.go` 中 `/fb/:slug` 处理器（lines 101-143）仍包含：

| 职责 | 所在位置 | 应属层级 |
|------|---------|---------|
| 项目数据获取 | routes.go:103: `application.DB.GetProjectBySlug(slug)` | app 层 |
| 项目不存在/已归档/已停用 错误渲染 | routes.go:107-125 | app 层 |
| 分类查询 + JSON 构建 | routes.go:127-140: `application.DB.ListCategories()` | app 层 |
| HTML 内容替换 | routes.go:141: `strings.Replace` | routes 层（可接受） |
| HTML 响应写入 | routes.go:142 | routes 层（可接受） |

- `setupGuard` 中内联了初始化状态检查和路由白名单（routes.go:47-66）
- Admin 路由 **不**混合业务逻辑 —— 全部委托给 handler_*.go 方法

**评估**：该函数体 ~40 行，在路由层中规模不算大，但确实跨了数据获取、错误处理和渲染三个职责域。建议将 `/fb/:slug` 的数据获取和错误处理抽象为一个 `app.PublicRenderFeedbackPage(slug)` 方法。

---

## 二、新功能模块评估

### ▎邀请系统（Invitation System）

| 文件 | 行数 | 评价 |
|------|------|------|
| `app/handler_invite.go` | ~270 行 | 🟢 清晰，handler → DB 分离 |
| `database/invitation.go` | ~114 行 | 🟢 规范的 DB 层，CRUD + 验证完整 |

**架构质量** 🟢 **良好**：
- 遵循项目既有模式：handler 调用 `a.DB.CreateInvitation()`、`a.DB.ValidateInvitation()` 等方法
- 数据库操作完全封装在 `database/invitation.go` 中
- 验证逻辑（过期检查、用量上限）放在 DB 层，允许其他调用方复用
- 注册流程完整：邀请验证 → 管理员创建 → 权限授予 → 使用计数

**注意点**：
- `project_ids` 以 JSON 字符串存储在 SQLite 列中，未做关系规范化 —— 在当前规模下合理，但无法执行引用完整性
- `InvitationToken.ProjectIDs` 字段在 handler 和 database 层都手动 `json.Unmarshal` —— 可考虑为 `InvitationToken` 添加 `ProjectIDsParsed() []string` 方法

---

### ▎表单字段注册表（Form Schema / Field Registry）

| 位置 | 功能 | 评价 |
|------|------|------|
| `Project.FormSchema` | 项目配置的字段定义 JSON | 🟢 以 JSON 文本存储在 projects 表 |
| `handler_misc.go:493` | `validateFormSchema()` 验证函数 | 🟢 支持 required/number/select/radio/email 校验 |
| `handler_feedback.go:64` | 提交时调用验证 | 🟢 验证点正确 |
| `routes/frontend/admin.html` | 前端字段编辑器 | 🟢 客户端拖拽排序 + 编辑界面 |
| `routes/frontend/feedback.html` | 动态渲染表单 | 🟢 根据 schema 动态生成表单 |

**架构质量** 🟢 **良好**：
- Schema 存储在 projects 表字段中，无需额外表结构
- 验证逻辑在 handler（业务逻辑）层，不在 DB 层 —— 职责恰当
- 前端表单动态渲染基于 `form_schema` JSON，后端提交时再次验证 —— 双层校验
- Schema 视为"项目配置"而非独立实体，与当前数据模型契合

---

### ▎RBAC 细粒度权限系统（Member Grants）

**架构质量** 🟢 **优秀**：

`(admin, project, category) → role` 三级模型设计全面：
- `database/admin.go`: `GetEffectiveRole()`、`GetAdminAccessPlan()`、`GetAllowedCategories()` 等方法完整
- `database/feedback.go`: `buildAccessPlanWhere()` 构建复杂的 WHERE 子句，支持 OR 逻辑和 IN 子句
- handler 层：`checkFeedbackReadPerm()`、`checkFeedbackWritePerm()` 在关键端点做权限拦截
- 路由层：`middleware.RequireRole("editor")` 和 `middleware.RequireRole("admin")` 中间件做前置守卫

**注意点**：对于轻量级反馈系统，三级 RBAC 可能过于复杂（上一轮已指出），但实现正确且测试覆盖充分。

---

### ▎大文件上传（MaxUploadSize）

**注意**：`SubmitFeedback` 中使用 `MaxBytesReader` 限制请求体大小（handler_feedback.go:25），但如果上传多个大文件，`MaxBytesReader` 可能在 `ParseMultipartForm` 解析前就截断请求 —— 需确认 `MaxUploadSize` 配置值是否足够大以避免用户混淆。

---

## 三、新发现的架构关注点

### 🔍 关注点 A：`InitDefaultConfig` 未持有 mu 锁

```go
// database/config.go:82-102
func (d *Database) InitDefaultConfig(cfg *config.Config) {
    // ...
    d.db.QueryRow(`SELECT COUNT(*) FROM config WHERE key = ?`, item.Key).Scan(&count)
    // ...
    d.SetConfig(item.Key, item.Value, item.Description) // SetConfig 内部会加锁
}
```

`InitDefaultConfig` 直接调用 `d.db.QueryRow` 而未持有 `d.mu`，而其他所有方法都通过 mu 保护。虽然该方法仅在启动时串行调用一次，但**与项目约定不一致**。

---

### 🔍 关注点 B：`QueryRaw` 的 RLock 释放时机

```go
// database/config.go:138-142
func (d *Database) QueryRaw(sql string, args ...interface{}) (*sql.Rows, error) {
    d.mu.RLock()
    defer d.mu.RUnlock()  // ← 在返回 *sql.Rows 之前释放
    return d.db.Query(sql, args...)
}
```

`defer d.mu.RUnlock()` 在 `QueryRaw` 返回时触发，但此时调用方还持有 `*sql.Rows`，底层 sql.DB 连接也被占用。在 `SetMaxOpenConns(1)` 模式下，后续写操作将阻塞等待连接被释放。**这不算数据竞争，但可能引入无谓的锁争用**。

---

### 🔍 关注点 C：`BackfillContentHashes` 长时间持写锁

```go
// database/feedback.go:462-493
func (d *Database) BackfillContentHashes() error {
    d.mu.Lock()
    defer d.mu.Unlock()
    // 先查询所有空 content_hash 的行
    // 然后逐行 UPDATE
}
```

该方法在写锁保护下完成全部查询和 N 次更新。仅在迁移时调用一次，所以实际影响有限。但应注释说明为什么持锁。

---

### 🔍 关注点 D：`ExportFeedbacks` 长时间持读锁

```go
// database/feedback.go:530-629
func (d *Database) ExportFeedbacks(projectID string) ([]Feedback, error) {
    d.mu.RLock()
    defer d.mu.RUnlock()
    // 主查询 + 投票数子查询 + 备注查询 + 评分查询
}
```

~100 行代码中持 RLock 完成了 4 个查询。对于导出操作可以接受，但如果在生产库数据量大时执行，会阻塞所有写操作较长时间。

---

## 四、八维度评估

### 1. 分层与职责

| 层级 | 评级 | 说明 |
|------|------|------|
| cmd/feedshit | 🟢 清晰 | 初始化编排，不包含业务逻辑 |
| config | 🟢 清晰 | 纯配置加载 |
| security | 🟢 清晰 | AES-GCM 加解密，单一职责 |
| database | 🟢 清晰 | 18 个文件，职责域明确 |
| middleware | 🟢 清晰 | 横切关注点分离良好 |
| app | 🟢 清晰 | 处理器层，职责恰当 |
| **routes** | 🟡 需关注 | `/fb/:slug` 仍含数据获取 + 渲染混合逻辑 |
| email | 🟢 清晰 | 模板 + SMTP 职责单一 |
| report | 🟢 清晰 | stats 采集已迁移至 database 层，job lock 仍绕行 |

**循环依赖检查**：✅ 无循环依赖

### 2. 数据流

| 路径 | 评级 | 说明 |
|------|------|------|
| 提交反馈 | 🟢 良好 | 验证 → PoW → 文件保存 → DB 写 → 异步通知 |
| 管理列表 | 🟢 良好 | RBAC 解析 → DB 读 → 分页 |
| 周报生成 | 🟢 良好 | 锁协调 → stats 采集 → 模板渲染 → 邮件发送 |
| 导出 | 🟢 良好 | ✅ 异步路径分离（goroutine 发送邮件/webhook） |

### 3. API 设计

| 维度 | 评级 | 说明 |
|------|------|------|
| RESTful 一致性 | 🟢 良好 | `/api/v1/` 版本化，资源路径一致 |
| 请求/响应格式 | 🟢 良好 | JSON 统一，错误信息友好 |
| 认证模式 | 🟢 良好 | Session + CSRF + API Token 三种认证路径清晰 |
| 速率限制 | 🟢 良好 | 双层限流（IP + Token），PoW 防滥用 |

### 4. 数据模型

| 维度 | 评级 | 说明 |
|------|------|------|
| Schema 设计 | 🟢 良好 | 22 步迁移，索引覆盖充分 |
| 迁移系统 | 🟢 优秀 | 版本化迁移，幂等设计，Go 数据回填 |
| 数据完整性 | 🟢 良好 | 外键约束（feedbacks → projects, votes → feedbacks） |
| 存储模式 | 🟢 务实 | JSON 列（form_schema, project_ids）适合当前规模 |

### 5. 并发模型

| 维度 | 评级 | 说明 |
|------|------|------|
| RWMutex 使用 | 🟡 冗余 | 单连接模式下外层 RWMutex 无并发收益 |
| 连接池配置 | 🟡 保守 | SetMaxOpenConns(1) 未利用 WAL 并发读 |
| goroutine 管理 | 🟢 良好 | 异步通知的 goroutine 生命周期管理恰当 |
| 锁协调 | 🟢 良好 | job_lock 表 + 15s 轮询，适合多实例部署 |

### 6. 扩展性

| 维度 | 评级 | 说明 |
|------|------|------|
| SQLite 约束认知 | 🟢 清醒 | 设计者清楚 SQLite 上限（100 万行/单节点） |
| 多实例 | 🟢 可行 | job_lock 表协调 + 文件系统共享需求 |
| 未来迁移路径 | 🟡 需准备 | DB 接口抽象缺失是迁移到 PostgreSQL 的最大障碍 |
| 文件存储 | 🟡 本地绑定 | 多实例需共享存储（NFS/S3） |

### 7. 安全架构

| 维度 | 评级 | 说明 |
|------|------|------|
| 存储加密 | 🟢 AES-256-GCM | 敏感配置项透明加密/解密 |
| 密钥管理 | 🟢 三重策略 | env var / file / auto-generate |
| 密码哈希 | 🟢 bcrypt | 行业标准 |
| SQL 注入 | 🟢 完美 | 全库无一例外参数化查询 |
| XSS/CSRF | 🟢 全面 | CSP + Double-Submit Cookie |
| 路径穿越 | 🟢 彻底 | filepath.Clean + EvalSymlinks 双重校验 |
| 速率限制 | 🟢 双层 | IP + Token 级别 |
| PoW 防滥用 | 🟢 有效 | SHA-256 + Nonce 缓存 |
| RBAC | 🟢 三级细粒度 | admin/admin → editor → viewer |
| 审计日志 | 🟢 完善 | 所有管理操作记录 |

### 8. 架构债

| 债项 | 类型 | 严重度 | 变化 |
|------|------|--------|------|
| 数据库层无接口抽象 | 欠抽象 | 🟡 中等 | 🔴 未改善 |
| ExecRaw/QueryRaw 绕行 | 分层违规 | 🟡 中等 | 🟡 主数据流已修复，job lock 仍绕行 |
| RWMutex + 单连接冗余 | 过度设计 | 🟢 轻度 | 🔴 未改善 |
| routes 混合业务逻辑 | 职责越界 | 🟢 轻度 | 🔴 未改善 |
| InitDefaultConfig 缺锁 | 不一致 | 🟢 轻度 | 🆕 新增关注 |
| QueryRaw RLock 释放时机 | 潜在争用 | 🟢 轻度 | 🆕 新增关注 |

---

## 五、总结评级

```
整体架构健康度: 🟢 良好
变化方向: → 持平（与上一轮比无显著改善也无退化）
```

| 评估维度 | 评级 | 变化 | 要点 |
|---------|------|------|------|
| 1. 分层与职责 | 🟢 良好 | → | routes 层轻微职责越界，其余清晰 |
| 2. 数据流 | 🟢 良好 | → | 异步路径分离，goroutine 用法合理 |
| 3. API 设计 | 🟢 良好 | → | RESTful 一致，版本管理到位 |
| 4. 数据模型 | 🟢 良好 | → | Schema 专业，迁移系统完善 |
| 5. 并发模型 | 🟡 需关注 | → | RWMutex 冗余未解决 |
| 6. 扩展性 | 🟢 良好 | → | DB 接口抽象缺失仍是未来隐患 |
| 7. 安全架构 | 🟢 优秀 | → | 全面防御，示范级轻量级项目 |
| 8. 架构债 | 🟢 低负债 | → | 无严重债务，3 项中等可改进项中 1 项部分改善 |

### 关键发现摘要

**最强项**（不变）：
1. **安全架构**：AES-256-GCM + bcrypt + XSS/SQLi/CSRF/路径穿越全防御 + 审计日志 —— 仍是轻量级项目中的标杆
2. **分层清晰度**：依赖图单向无循环，各包职责边界明确
3. **迁移系统**：22 步版本化迁移，幂等设计，Go 数据回填

**改善项**：
- `report` 包的周报统计采集（`collectWeeklyStats`）从 `ExecRaw`/`QueryRaw` 迁移为规范 database 方法调用 ✅

**未改善项**：
- 数据库层接口抽象、RWMutex 冗余、routes 层业务逻辑混合 —— 三项均未涉及

**新增关注**：
- `InitDefaultConfig` 未遵循项目的 mu 锁约定
- `QueryRaw` 的 RLock 在 `*sql.Rows` 被消费前释放，在单连接模式下可能引入不必要的争用
- 邀请系统 `project_ids` 以 JSON 存储，无法执行引用完整性

### 一句话评价

> FeedShit 保持了良好的架构健康度，安全防线仍是同类项目中的典范。上一轮识别的三项中等架构债**基本未改善**，但新功能（邀请系统、表单 schema、RBAC 细粒度权限）模块化做得干净，未引入新问题。建议在下个迭代中优先解决**数据库接口抽象**（系统性重构障.碍）和 **ExecRaw/QueryRaw 绕行**（剩余 job lock 部分）。
