# FeedShit 代码审查报告（仅查看，未修改）

审查范围：`internal/`（app、config、database、email、middleware、routes）、`cmd/feedshit/main.go`、内嵌前端（`admin.html` / `feedback.html` / `setup.html` / `track.html`）。
结论：核心提交→审核→回复→状态→通知→追踪闭环**功能完整且可行**；安全基础（CSRF、防暴破、PoW、参数化 SQL、路径穿越防护）实现正确；但存在 2 个高严重度功能缺陷（数据库备份失效、邮件模板形同虚设）和若干中/低缺陷与可优化点。

---

## 一、已确认正确 / 做得好的地方

- **无 SQL 注入**：所有查询均参数化；`SearchFeedbacks` 的 `LIKE` 也是参数绑定；动态列来自固定 `cols` 常量。
- **CSRF（双重提交 Cookie）正确**：`admin_session` 为 `httpOnly`，`csrf_token` 为非 httpOnly 供 JS 读取；中间件对 GET/HEAD/OPTIONS 跳过，对写方法校验 `Cookie == X-CSRF-Token`（用 `SecureCompare`）。前端 `apiJSON()` 统一通过 `getCsrfHeaders()` 携带 `X-CSRF-Token`。
- **登录防暴破生效**：`LoginAttemptTracker` 的 `IsLocked(ip)` 与 `RecordFailure(ip)` 使用一致的 key（均为 IP），且成功后 `ClearFailures`；并有 15 分钟清理循环。
- **PoW + 速率限制**：`/api/v1/feedback/submit` 与 `/api/v1/track/reply` 共用每 IP 10 次/小时限制（见下“可优化”），含 nonce 重放防护。
- **路径穿越防护**：`AdminServeFile` 用 `filepath.EvalSymlinks` + 前缀校验，Windows 下安全；上传 SVG 做了脚本清理。
- **邮件内容转义**：`SendFeedbackNotification` 对标题/描述/IP 均 `html.EscapeString`，无邮件 XSS。
- **安全响应头**：`X-Content-Type-Options`、`X-Frame-Options`、`Referrer-Policy`、`Content-Security-Policy` 已设置。

---

## 二、确定的 Bug（按严重度）

### 高：数据库备份完全失效
`internal/database/database.go:1104`
```go
_, err := d.db.Exec(`VACUUM INTO ?`, backupPath)
```
SQLite 的 `VACUUM INTO` **要求文件名是字符串字面量，不接受 `?` 绑定参数**，prepare 阶段即报错。结果：`AdminBackup` 永远返回错误→管理员点“备份”必失败；`AdminPruneOldBackups` 也因无备份文件可清理而失效。
**修复**：使用安全转义的字面量，例如
```go
safe := strings.ReplaceAll(backupPath, "'", "''")
_, err := d.db.Exec(fmt.Sprintf("VACUUM INTO '%s'", safe))
```
或改为：拷贝 `.db` 文件 + `PRAGMA wal_checkpoint(TRUNCATE)`。建议同时增加备份成功/失败的审计日志与失败告警。

### 高：邮件模板配置形同虚设（保存无效）
- 保存：`AdminUpdateEmailTemplate`（`app.go:2757`）把 `email_template_subject`、`email_template_body` 写入 config。
- 实际使用：`email.go` 的 `SendFeedbackNotification`（新反馈通知）与 `app.go` 的 `AdminUpdateFeedbackStatus` / `AdminAddFeedbackNote`（状态/回复通知）**均用硬编码模板**，`getEmailConfig()` 从不读取这两个 key。
即“邮件模板”设置页保存后**任何效果都没有**，是死功能。
**修复**：邮件服务读取 `email_template_subject`/`email_template_body`，并对 `{{title}}`/`{{status}}`/`{{link}}` 等占位符做替换；无配置时回退到默认模板。

### 中：超级管理员未写入 `admins` 表
`DoSetup`（`app.go:789`）只写 config（`admin_username`/`admin_password`），**不插入 `admins` 行**。`AdminLogin` 通过 legacy 分支（读 config）仍可登录，但带来不一致：
- `/api/v1/admin/admins` 列表为空，超级管理员不可见；
- 无法在 UI 中为其分配项目权限（`AdminGetProjectMembers` 返回 404）；
- 团队管理（角色/禁用/删除）对其无效。
- 注：密码修改走 config 分支仍可用，不受影响；但管理列表不一致属真实缺陷。
**修复**：`DoSetup` 同时插入一条 `role=admin` 的 `admins` 记录，统一数据模型。

### 中：多项目受限管理员的翻页/计数错误
`AdminListFeedbacks`（`app.go:474`）：当 `len(allowedSlugs) > 1` 时，先按**全局** `LIMIT/OFFSET` 取一页，再在内存过滤。`total` 变成“该页过滤后数量”而非真实总数，且翻页会跳过/丢失跨页的被允许项目数据。
**修复**：把项目白名单下推到 SQL：`WHERE project_id IN (?,?,...)`，并在 DB 层按白名单计数。

### 中：外部 API 提交缺少体积上限与项目校验
`SubmitFeedbackWithToken`（`/api/v1/feedback/submit/:token`）：无 `MaxBytesReader`，可信 token 可提交超大 multipart 造成内存压力；也未校验 `project` 是否存在/启用，可能写入“孤儿反馈”。
**修复**：复用 `MaxBytesReader`；提交前校验项目存在且 `is_active=1`。

### 中：CSV 导入脆弱
`AdminImportCSV` 依赖中文表头（"项目"/"状态"/"标题"等），否则字段静默为空；项目 slug 不存在时 `projectID` 为空→写入孤儿反馈；`created_at` 仅支持 `2006-01-02 15:04:05` 一种格式，失败即静默用当前时间。
**建议**：同时支持中英文表头、更宽松的时间解析、对空/孤儿 project 显式报错或拒绝。

### 低 / 代码质量
1. `LoginAttemptTracker.lockout` 字段（`middleware.go:245`）从未被使用——锁定时长由滑动窗口决定（≈15 分钟，从首次失败算起），与字段语义不一致。
2. `AdminServeFile`（`app.go`）先读 `c.Param("filepath")`（恒为空，路由参数为 `path`）再回退到 `c.Param("path")`，为误导性死代码，功能正常但可读性差。
3. `buildFeishuCard` 的 `baseURL` 参数未使用（死代码）。
4. `AdminUpdateFeedbackStatus` 只要请求带 `status` 就向提交者发邮件，即使状态未变化（重复提交同状态）也会打扰。
5. `AdminListFeedbacks` 同一请求内两次调用 `GetAdminByUsername` + `GetAdminProjectSlugs`，可合并。
6. `feedbacks` 表无 `updated_at` 列，列表/详情响应中的 `updated_at` 不会被填充（结构体有字段但未在 SQL 选择），前端“更新时间”显示为 0/空。如需展示，应加列并在更新时维护。

---

## 三、功能完整性与可行性评估

| 模块 | 状态 | 说明 |
|---|---|---|
| 公开提交（多字段/上传/联系人/PoW） | 可用 | 表单 schema 来自 `projects.form_schema`，逻辑闭环完整 |
| 提交后追踪（按 token 查状态/公开回复） | 可用 | `PublicTrackFeedback` 返回状态与公开 notes |
| 后台列表/详情/筛选/搜索/批量 | 可用 | 多项目受限角色有翻页 bug（见上） |
| 状态变更 / 指派 / 优先级 / 笔记 | 可用 | 含邮件与 Webhook 通知 |
| 邮件通知（SMTP） | 部分 | 发送链路可用，但“邮件模板”配置无效 |
| 飞书 / Webhook 通知 | 可用 | 含重试与 metrics；Webhook 建议加 HMAC 签名 |
| 数据库备份 / 清理 | 失效 | `VACUUM INTO ?` 不被支持 |
| 团队/角色管理 | 部分 | 超级管理员不在 `admins` 表，管理不一致 |
| 外部 API（token 提交/查询/项目列表） | 可用 | 缺体积上限与项目校验 |
| 统计 / 图表 / 审计日志 / 指标 | 可用 | metrics 端点已具备 |
| 自动化归档（旧反馈） | 可用 | 按天/状态归档 |
| 单元测试 | 缺失 | `test/` 下无 `_test.go`，仅示例 db/exe/bat |

整体**可行**，核心业务闭环可上线使用；上线前应至少修复两个高严重度缺陷。

---

## 四、优化与功能建议

**稳定性 / 正确性（优先级高）**
1. 修复备份（`VACUUM INTO` 字面值或文件拷贝 + checkpoint）+ 备份结果审计/告警。
2. 邮件模板真正生效（变量占位、HTML/纯文本双版本、按项目自定义）。
3. 超级管理员统一入 `admins` 表。
4. 项目级访问控制下推 SQL（`IN` 子查询）修复翻页。
5. 外部 API 增加体积上限与项目存在性/启用校验。

**安全增强**
6. Webhook 增加 HMAC 签名校验与按事件类型订阅；失败入告警。
7. 速率限制按业务拆分（提交 vs 回复）或按项目/用户维度，避免共享 10/h 互相挤占。
8. 登录失败锁定时长用显式 `lockUntil` 字段，避免依赖滑动窗口导致“锁定时长不确定”。
9. `GetClientIP` 默认 `trustedProxies` 为空→不会信任头部，安全；若启用 CDN 自动识别，建议文档化风险。

**产品 / 体验**
10. 提交端防重复提交（`client_token` 去重）、按项目自定义提交频率、敏感词/垃圾检测。
11. 反馈标签/分类、优先级 SLA、负责人到期提醒（邮件/飞书）。
12. 统计增强：来源分布、响应时长（中位数/分位）、按当前筛选条件导出 CSV。
13. 可观测性：metrics 接 Prometheus；日志结构化；备份/通知失败汇总看板。
14. 国际化：前端硬编码中文，建议抽离 i18n；暗色模式、批量操作二次确认、键盘快捷键。

**工程化**
15. 补充单元测试与集成测试（handler / middleware / database / PoW / CSV 导入）。
16. 前端从内嵌 HTML 抽离为独立构建（当前维护成本高）。
17. 部署：`Dockerfile`/`docker-compose` 已具备；建议增加 healthcheck、非 root 运行、只读配置挂载、HTTPS 反代示例。

---

## 五、需重点关注的修复顺序
1. 数据库备份（`VACUUM INTO ?`）
2. 邮件模板配置生效
3. 超级管理员入表 + 团队管理一致性
4. 多项目翻页下推 SQL
5. 外部 API 体积上限 + 项目校验

---

## 六、修复状态核对（2026-07-19，仅查看）

`go build ./...` 与 `go vet ./...` 均退出码 0，修复可编译且通过静态检查。逐项核对：

| # | 缺陷 | 位置 | 状态 | 核对要点 |
|---|---|---|---|---|
| 1 | 数据库备份失效 | `database.go:1120-1122` | ✅ 已修复 | `VACUUM INTO` 改用转义后的字符串字面量 `VACUUM INTO '%s'`（单引号转义），符合 SQLite 语法 |
| 2 | 邮件模板形同虚设 | `email.go` + `app.go:626-627,1666-1667` | ✅ 已修复 | `SendFeedbackNotification` 改调 `BuildNotificationSubject/Body`；状态/回复通知的调用方也改用 `BuildStatusChange*/BuildReply*`，均读取 `email_template_subject/body` 配置 |
| 3 | 超级管理员未入 `admins` 表 | `app.go:812-815` | ✅ 已修复 | `DoSetup` 新增 `a.DB.CreateAdmin(username, hashedPwd, "admin")`，团队管理列表/权限可见 |
| 4 | 多项目受限角色翻页错误 | `app.go:474-555` + `database.go:317-327,367-377` | ✅ 已修复 | 列表构建 `projectIDs` 并下推到 `ListFeedbacks`/`SearchFeedbacks`；`project_id IN (...)` 同时作用于数据查询与 `COUNT(*)`，`total` 准确 |
| 5 | 外部 token 提交无体积上限 | `app.go:2407` | ✅ 已修复 | 增加 `MaxBytesReader(..., 1<<20)`（1MB） |
| 6 | CSV 导入脆弱 | `app.go:2591-2720` | ✅ 已修复 | 增加中→英表头别名映射、多时间格式解析、`created_at` 不再静默篡改；逐行校验项目存在/启用并显式报错 |
| 7 | `feedbacks` 无 `updated_at` 列 | `database.go:163,207` | ✅ 已修复 | 表结构与迁移均新增 `updated_at`；各 `UPDATE` 维护 `updated_at = strftime('%s','now')` |

### 修复后仍需注意的轻微残留项（非阻塞）
- **自定义邮件模板未转义**：`renderTemplate` 直接把 `{{description}}`/`{{note_content}}` 等提交者内容插入 HTML 邮件；默认模板做了 `html.EscapeString`，但自定义模板不做。若管理员设置了含 `{{description}}` 的自定义模板，提交者可借此在管理员邮箱注入 HTML。建议自定义模板替换前也对用户字段做转义。
- **外部 token 提交限制硬编码 1MB**：未复用 `MaxUploadSize` 配置；若配置上限大于 1MB，外部带附件上传会被拒。
- **CSV 无 project 列且无表单 project_id 时**：回退到 `"default"` slug 但未校验其存在，可能写入孤儿反馈。
- **备份目标文件冲突**：`VACUUM INTO` 要求目标不存在；当前按秒级时间戳命名，实际上不会冲突。
