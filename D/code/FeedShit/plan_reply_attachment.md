# 回复附件持久化（#4 完整实现）改动计划

> 状态：待审核（未实施）
> 目标：把追踪页回复的"文件上传"从纯提示做成真正可用的功能——提交者上传 → 落盘 → 提交者本人与管理员都能在对应回复下看到并可下载。

---

## 一、背景与范围

上一轮 #4 只加了"大小/类型提示"的前端文案（`file-hint` + `accept` + 前端 20MB 拦截），但后端 `PublicSubmitReply` 至今只存文本、忽略 `file` 字段——用户上传的截图/日志并没有真正落库，追踪页与后台都看不到。

本计划把回复附件做成**真正可用**：
- 提交者回复可附带文件 → 落盘 → 提交者本人在追踪页该回复下看到 chip 并可下载 → 管理员在后台反馈详情的回复线程里同样可见并可下载。

**范围**：提交者回复（track 页 #4 主体）。管理员回复也带附件列为**可选 Phase 2**（见末尾），不在本轮强制范围。

---

## 二、现状盘点（已逐行核实代码）

| 项 | 位置 | 现状 |
|----|------|------|
| 回复提交 | `handler_track.go:131` `PublicSubmitReply` | 仅 `c.PostForm("content")`，无 multipart、无文件处理，忽略 `file` |
| 落盘能力 | `handler_misc.go:147` `saveUpload` | **已具备**：扩展名白名单 + 魔数校验 + SVG 净化 + 按 `uploads/<projectID>/年/月/日/<uid>/` 落盘，返回相对路径；原反馈提交 `SubmitFeedback` 已用它 |
| 类型白名单 | `handler_misc.go:142` `allowedExtensions` | `.png .jpg .jpeg .gif .webp .bmp .svg .log .txt .csv .json` |
| 前端 accept | `track.html:151` | `image/*,.pdf,.doc,.docx,.txt,.zip` |
| 备注表 | `feedback_notes` | 无文件列；`FeedbackNote` 结构体无 `file_paths` |
| 文件服务 | `routes.go:300` `/files/*` | 挂在 admin 页面 catch-all，需 `admin_session`，**提交者用不了** |
| 前端发件 | `track.html:336` `doReply` | 已用 FormData 把 `file` 发出（multipart 已就绪），仅需后端接收 |

### ⚠️ 关键坑：前后端类型不一致
前端提示支持 `.pdf/.doc/.docx/.zip`，但后端 `allowedExtensions` **不含这四种**。用户按提示选 pdf/zip 会被后端返回「不允许的文件类型」。本计划必须对齐两者。

---

## 三、设计决策

1. **存储粒度**：附件挂在 `feedback_notes` 行（每条回复各自附件），与反馈主表 `feedbacks.file_paths`（原始提交附件）区分，互不影响。
2. **类型白名单扩展**：把 `.pdf .doc .docx .zip` 加入 `allowedExtensions`，并在 `validateFileContent` 补对应魔数（PDF=`%PDF`、ZIP/DOCX=`PK\x03\x04`、DOC=OLE2 `D0 CF 11 E0`）；前端 `accept` 同步更新。让"截图/日志/文档/压缩包"都可用，且真正校验内容。
3. **提交者侧下载路由**：新增 `GET /api/v1/track/file?token=&note=&i=`，**路径完全由 DB 派生**（取 `note.file_paths[i]`，不信任任何客户端路径）→ 无遍历面；再做与 `AdminServeFile` 同款的 `filepath.Clean` + `EvalSymlinks` + `uploads/` 前缀校验后 `c.File`。提交者只能下到自己反馈的附件。
4. **安全**：请求级 `http.MaxBytesReader` + 单文件 ≤ `MaxUploadSize`（沿用 20MB 配置）；类型走 `saveUpload` 既有校验；SVG 已净化。

---

## 四、实施步骤

### 步骤 1 — 数据库迁移 v28
- `internal/database/migrate.go` 新增 `{28, "feedback_notes file_paths", ...}`：
  ```sql
  ALTER TABLE feedback_notes ADD COLUMN file_paths TEXT NOT NULL DEFAULT '[]';
  ```
- SQLite 不支持 `ADD COLUMN IF NOT EXISTS`，加幂等保护：先
  `SELECT COUNT(*) FROM pragma_table_info('feedback_notes') WHERE name='file_paths'`，为 0 才执行 ALTER（防本地重复跑 migrate 报错；生产按 id 顺序只跑一次本就安全）。
- `internal/database/database.go:88` `FeedbackNote` 结构体加 `FilePaths string \`json:"file_paths"\``。

### 步骤 2 — 数据层（note.go / database.go）
- `InsertFeedbackNote(feedbackID, content, author, isPublic, filePaths string)`：INSERT 增 `file_paths` 列。
- `InsertSubmitterReply(feedbackID, content, filePaths string)`：INSERT 增 `file_paths`，保持 `author='提交者', is_public=1`。
- `ListFeedbackNotes` / `GetFeedbackNote`：SELECT 增 `file_paths`（放 `is_public` 后、`created_at` 前），Scan 补齐。
- grep 所有 `InsertSubmitterReply` / `InsertFeedbackNote` 调用方（含测试）补第 5 参数。

### 步骤 3 — 后端 handler
- **`PublicSubmitReply` 改造**：
  - `c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, a.Cfg.MaxUploadSize)` + `c.Request.ParseMultipartForm(a.Cfg.MaxUploadSize)`（失败 → 413）。
  - `content := c.PostForm("content")`；保留空/2000 字校验；**允许"仅附件、无正文"**（有文件且 content 为空时放行，贴合截图回复场景）。
  - `files := c.MultipartForm().File["file"]`；逐文件 `a.saveUpload(fh, fb.ProjectID)`（用 `fb.ProjectID` 进 uploads 目录，与 SubmitFeedback 一致）；单文件 > `MaxUploadSize` 直接拒；收集相对路径。
  - `pathsJSON, _ := json.Marshal(paths)`；`InsertSubmitterReply(fb.ID, content, string(pathsJSON))`。
  - 保留 `ReplyLimiter`、`SendSubmitterReplyNotification`、webhook（payload 可加 `files` 路径数组）。
- **新增 `PublicServeTrackFile(c)`**：
  - 解析 `token` → `GetFeedbackByTrackingToken`；解析 `note` id + `i` index。
  - 校验该 note 属于该 feedback（防越权）；取 `note.file_paths[i]`，为空/越界 → 404。
  - 走安全落盘校验（`Clean` + `EvalSymlinks` + `uploads/` 前缀）后 `c.File(absPath)`。
- **`PublicTrackFeedback`**：无需改映射——`notes` 直接序列化 `[]FeedbackNote`，`FilePaths` 字段自动带出（json tag `file_paths`）。

### 步骤 4 — 前端 track.html
- `renderFeedback` 渲染 note 时：若 `n.file_paths` 数组非空，在内容下方渲染附件 chips，链接 `/api/v1/track/file?token=<currentToken>&note=<n.id>&i=<i>`（新窗口打开）。
- 更新 `accept`（与扩展后白名单一致：图片/PDF/Word/文本/日志/CSV/JSON/压缩包）与 `file-hint` 文案（单文件 ≤20MB）。
- `doReply` 已正确发 multipart，无需大改；确认单文件 >20MB 前端拦截保留。

### 步骤 5 — 后台（admin）可见性
- `shared/admin/dashboard.js` 渲染回复线程时，对每条 note 的 `file_paths` 渲染下载 chip（复用 `/files/<path>`，admin 已鉴权）。
- （可选 Phase 2）管理员回复也支持附件：扩展 admin 回复表单 + `app.go:119` 的 `InsertFeedbackNote` 第 5 参数。

### 步骤 6 — 路由
- `internal/routes/routes.go`：`trackReply.GET("/file", application.PublicServeTrackFile)`。

---

## 五、验证

- `go build ./...` / `go vet ./...` / `go test ./...`（9 包）全绿。
- 临时端到端测试（运行后删）：
  1. multipart `POST /api/v1/track/reply` 带 1 张 PNG + 1 个 .txt → 200；DB 该 note `file_paths` 含 2 项。
  2. `GET /api/v1/track/file?token=&note=&i=0` → 200 且字节一致。
  3. 用**另一条反馈的 token** 请求上述路径 → 404（越权拦截）。
  4. 超限文件 / 非法类型（如 `.exe`）→ 4xx。
  5. 迁移：全新库 + 已含 v27 的库各跑一次 migrate，确认 v28 幂等、无报错。
- 前端：`node --check` track.html 脚本；手动上传 → 看到 chip → 点击下载。
- 冒烟：同调用内启动二进制 → 提交带附件回复 → curl `/api/v1/track/feedback?token=` 确认 note 返回 `file_paths` → curl `/api/v1/track/file` 下载成功。

---

## 六、风险与权衡

- **类型扩展须同步魔数校验**：加 `.pdf/.doc/.docx/.zip` 必须补 `validateFileContent`，否则仅扩展名白名单会被魔数拦下。
- **迁移幂等**：v28 的 ADD COLUMN 需列存在性保护，防本地重复跑 migrate 报错（生产按 id 顺序只跑一次，本就安全）。
- **存储增长**：附件入 `DataDir/uploads`，随反馈量增长；现有 `BACKUP_RETENTION_DAYS` 只管 DB 备份、不含 uploads。如需可后续加上传清理/容量上限（独立 task）。
- **单实例约束**：沿用现有 `saveUpload`，无新增进程内存状态，符合单实例天花板。

---

## 七、提交建议

实现完成后本地提交（沿用上轮 `f36849e` 风格的单次提交），排除 `plan_*.md` 类 artifact；不推送，待你确认。
