# FeedShit 架构改进 · 阶段0 + 阶段1 产品需求文档（简单 PRD）

> 文档类型：简单 PRD（纯后端 / 运维加固，无前端界面变更）
> 作者：许清楚（产品经理）
> 日期：2026-07-19
> 权威来源：`deliverables/engineering-assurance/architecture-review-feedshit-2026-07-19.md`
> 代码基线：`D:\code\FeedShit\` 当前工作区（已对照架构评审报告复核）

---

## 0. PRD 摘要（结论先行）

**目标一句话**：把架构评审给出的"加固路线"转化为可验收的需求条目，使 FeedShit 达到**安全基线 + 可交付 CI 门禁 + 可运维**状态（不重构、不引入新功能）。

**条目规模**：
- **P0（阶段0，发布闸门）共 4 组 / 6 条需求**：CI 门禁、安全关键纯函数单测、密钥不落库/加密（拆 3 子项）、legacy webhook 签名或下线。
- **P1（阶段1，上线前加固）共 5 组 / 6 条需求**：单实例 runbook、外部接口默认限速、迁移错误不再静默、备份自动清理、镜像 tag + `.env.example`（拆 2 子项）。

**关键待确认决策（须主理人/用户拍板，详见第 4 节）**：
1. **密钥加密方案**：AES-GCM + 主密钥 `FEEDSHIT_MASTER_KEY`（仅环境变量）？还是完全不落库、仅环境变量？还是其他？
2. **legacy webhook 处理**：为全局 `webhook_url` 出站加 HMAC 签名（需配 legacy secret）？还是直接下线、统一走订阅式 `/api/v1/admin/webhooks`？
3. **CI 平台**：默认 GitHub Actions？（项目托管于 `github.com/Asunano/FeedShit`）
4. **单元测试范围**：仅安全关键纯函数？还是扩展到 RBAC 权限计算 / 集成测试？

---

## 1. 项目信息

| 项 | 内容 |
|----|------|
| 语言（文档） | 中文（与需求一致） |
| 编程语言 | Go 1.26.5（`go.mod` 第 3 行）；纯后端 / 运维加固，**无 UI 框架**；测试使用标准库 `testing` |
| 项目名（snake_case） | `feedshit_phase01_prd` |
| 原始需求复述 | 将架构评审报告的阶段0（P0 安全加固+补测试）与阶段1（P1 运维加固）共 9 条建议，细化为带验收标准、涉及文件、优先级的需求条目，作为架构设计与工程实现的输入。不做架构设计、不写代码、不新增业务功能。 |
| 范围边界 | 仅覆盖阶段0 + 阶段1；阶段2+（M1/M5/M8 等增量功能）与 god-object 拆分（阶段4）不在本 PRD。 |

---

## 2. 产品定义

### 2.1 产品目标（3 个正交目标）

1. **安全基线**：消除敏感凭据（SMTP 密码 / Webhook secret / API token）明文落库与未签名出站通道，建立凭据最小暴露面。
2. **可交付门禁**：建立 CI（build/vet/test）+ 安全关键纯函数单元测试，使每次变更可被回归测试阻断、发布可审计。
3. **可运维性**：书面化单实例硬约束、自动清理备份、不可变镜像与配置模板，降低误配与运维事故风险。

### 2.2 用户故事（6 条）

| # | 角色 | 用户故事 |
|---|------|----------|
| US-1 | 安全工程师 | 作为安全工程师，我希望 SMTP 密码 / Webhook secret / API token 不以明文存入数据库或备份，以免 DB 文件或备份泄露即泄露凭证。 |
| US-2 | 运维 | 作为运维，我希望有书面 runbook 明确"禁止水平扩展（scale>1）"及其原因，以免误配多副本导致会话漂移、限流失效、Webhook 重复投递。 |
| US-3 | 运维 | 作为运维，我希望备份能按保留策略自动清理，以免磁盘无限增长导致服务不可用。 |
| US-4 | 开发者 | 作为开发者，我希望每次 push/PR 都有 CI 跑 build/vet/test，且安全关键纯函数有单测回归，以免改动无声引入越权或崩溃。 |
| US-5 | 系统管理员 | 作为系统管理员，我希望外部 API token 有默认速率上限，以免 token 泄露后被滥用刷量。 |
| US-6 | 安全工程师 | 作为安全工程师，我希望 legacy webhook 出站带签名或彻底下线，以免通知管道被伪造 / 中间人冒用。 |

---

## 3. 需求池

> 优先级说明：**P0 = 发布前必修（阻塞）**；**P1 = 上线前条件项**；**P2 = 后续迭代非阻塞**。
> 涉及文件均经实际读码确认（附 `file:line` 证据）。

### 3.1 P0 — 阶段0（发布闸门，约 3–5 天）

#### REQ-CI-01 · 建立 CI 门禁
- **描述**：新增 GitHub Actions workflow，在 `push` 与 `pull_request` 触发，依次执行 `go build ./...`、`go vet ./...`、`go test ./...`。
- **验收标准**：
  1. `.github/workflows/ci.yml` 存在且语法有效；
  2. 任一阶段失败则 CI 标红（exit non-zero）；
  3. 工作流在 PR 检查中可见且为必过项（若仓库开启 branch protection）。
- **涉及文件**：`.github/workflows/ci.yml`（新增）；`go.mod`（Go 1.26.5）。
- **优先级**：P0

#### REQ-TEST-01 · 安全关键纯函数单元测试
- **描述**：为下列已确认的无副作用纯函数补充表驱动单元测试（含边界与攻击向量），可借助已有的 `database.NewTestDatabase()`（`internal/database/database.go:145`）做轻量集成。
  | 函数 | 文件:行 | 测试重点 |
  |------|---------|----------|
  | `VerifyPoW` | `internal/middleware/middleware.go:419` | 难度位、时间戳窗口、nonce 重放、篡改输入 |
  | `SecureCompare` | `internal/middleware/middleware.go:449` | 等长/不等长、恒定时间、前后缀差异 |
  | `RequireRole` | `internal/middleware/middleware.go:143` | 角色层级 gate（admin>manager>editor>viewer）放行/拦截 |
  | `renderTemplate` | `internal/email/email.go:136` | 占位符替换、用户字段 HTML 转义、URL 字段不转义 |
  | `sanitizeSVG` | `internal/app/app.go:350` | 剥离 `<script>`、事件属性（`on*`）、`javascript:` 链接 |
  | `validateFileContent` | `internal/app/app.go:362` | 各扩展名魔数匹配 / 非匹配拒绝 |
  | `buildAccessPlanWhere` | `internal/database/database.go:413` | 通配 `*` 与分类级 RBAC 生成的 SQL WHERE 正确性 |
- **验收标准**：
  1. 上述每个函数至少有一个 `*_test.go` 测试文件与多个用例；
  2. `sanitizeSVG` 用例须覆盖 `<script>`、`<img onerror=...>`、`href="javascript:..."` 三类；
  3. `go test ./...` 通过，且生成覆盖率报告（建议 `go test -cover`）。
- **涉及文件**：各函数所在包新增 `middleware/*_test.go`、`email/*_test.go`、`app/*_test.go`、`database/*_test.go`；复用 `database.NewTestDatabase()`。
- **优先级**：P0

#### REQ-SECRET-01 · 密钥存储方案与格式（密钥不落库/加密 — 方案层）
- **描述**：确定敏感凭据的存储形态，确保 `config` 表与 `webhook_subscriptions`/`api_tokens` 表不再以明文保存凭据。覆盖对象与现状证据：
  - SMTP 密码：`config.go:41` 从 `SMTP_PASS` 读取 → `database.go:775` `InitDefaultConfig` 写入 `config` 表 `smtp_pass`；`email.go:37` `getEmailConfig` 读取明文（`email.go:122`）。
  - legacy `webhook_url`：`config.go:49` 从 `WEBHOOK_URL` 读取；`app.go:1462` / `app.go:1615` 从 `config` 表读取（明文），回退到 `cfg`。
  - 订阅式 `webhook_subscriptions.secret`：明文存储（`database.go:357` 建表、`database.go:371` outbox），读取时 `maskSecret` 掩码（`app.go:1727`）但库内仍是明文。
  - `api_tokens.token`：明文存储、按 token 直查（`database.go:1915`）。
- **验收标准**：DB 导出 / 备份文件中不包含上述凭据的明文；存储格式与读取路径在方案确定后落地（见 REQ-SECRET-02/03）。
- **涉及文件**：`internal/config/config.go`、`internal/database/database.go`、`internal/email/email.go`、`internal/app/app.go`。
- **优先级**：P0
- **关联决策**：第 4 节 (a)。

#### REQ-SECRET-02 · 凭据读取与解密路径
- **描述**：运行时凭据仅从环境变量 / 主密钥解密读取，**`config` 表不再作为明文凭据源**。`InitDefaultConfig`（`database.go:775`）不再把 `smtp_pass` 以明文 seed 进 `config` 表；`getEmailConfig`（`email.go:122`）改为从解密源读取。若采用"加密落库"方案，则 `SetConfig`（`database.go:735`）写入前加密、`GetConfig`（`database.go:725`）读取后解密。
- **验收标准**：
  1. 进程启动后 `config` 表 `smtp_pass` / `webhook_url` 字段不为明文（或为空、仅留密文）；
  2. 所有读取路径能正确解密并可用（邮件能发送、webhook 能投递）；
  3. 主密钥缺失时启动失败（fail-fast）而非静默用空凭据。
- **涉及文件**：`internal/config/config.go`、`internal/database/database.go:725/735/770`、`internal/email/email.go:122`、`internal/app/app.go:1462/1615`。
- **优先级**：P0

#### REQ-SECRET-03 · 后台配置界面的凭据掩码
- **描述**：若凭据仍经后台 UI 配置（邮件/系统配置、webhook 订阅），则必须加密存储，且在读取/列表时掩码展示（沿用现有 `maskSecret` 模式，`app.go:1727`），且不提供明文回显接口。
- **验收标准**：UI/API 返回中凭据字段为掩码；更新接口接受明文并加密落库；不存在返回明文凭据的接口。
- **涉及文件**：`internal/app/app.go`（Admin 配置/Webhook 订阅 handler）、`internal/database/database.go`。
- **优先级**：P0

#### REQ-WH-01 · legacy webhook 签名或下线
- **描述**：当前全局 `webhook_url` 出站走 `EnqueueRawWebhook(webhookURL, "", payload)`（`app.go:1668-1673`），空 secret → `deliverWebhook` 不加 `X-FeedShit-Signature`（`database.go:2012` 写入 `secret=''`）。二选一：
  - **方案 A（签名）**：为 legacy 通道配置 secret，出站时与订阅式一致带 HMAC 签名；
  - **方案 B（下线）**：移除 legacy 出站分支，统一走订阅式 `/api/v1/admin/webhooks`（`routes.go:290-293`）。
- **验收标准**：
  - 选 A：`deliverWebhook` 对 legacy 出站也带 `X-FeedShit-Signature`，接收方可验真；
  - 选 B：`app.go:1668-1673` 分支移除，文档说明迁移路径，无"no secret → no signature"的明文出站。
- **涉及文件**：`internal/app/app.go:1664-1673`、`internal/database/database.go:2012`、`internal/routes/routes.go`（可选 UI）。
- **优先级**：P0
- **关联决策**：第 4 节 (b)。

### 3.2 P1 — 阶段1（上线前加固，约 3 天）

#### REQ-RUNBOOK-01 · 单实例约束 runbook
- **描述**：新增运维 runbook，书面明确"**禁止水平扩展（scale>1）**"——原因：会话 / 限流 / nonce / 登录锁 / webhook 投递全为内存态，多副本会失效（`architecture-review` §3.6）。文档须含：单副本部署、数据卷挂载、备份与恢复、升级/回滚流程、为何不能多副本。
- **验收标准**：
  1. `docs/runbook.md`（或项目约定路径）存在；
  2. 明确写出"禁止 scale>1"及技术原因；
  3. `README.md` 部署章节引用该 runbook。
- **涉及文件**：`docs/runbook.md`（新增）、`README.md`（引用）。
- **优先级**：P1

#### REQ-RL-01 · 外部接口默认限速
- **描述**：外部 API token 通道当前仅当 `token.RateLimit>0` 才限流（`app.go:3162-3174`），默认 0 = 不限；路由层虽挂了 IP 级 `RateLimitMiddleware`（`routes.go:154-157`），但缺 token 级默认上限。新增系统级默认（如环境变量 `API_TOKEN_DEFAULT_RATE_LIMIT`），创建 token 时默认非 0，或中间件对未显式设限的 token 套用兜底上限。
- **验收标准**：
  1. 默认新建 API token 带速率上限（非 unlimited）；
  2. 超限返回 `429`；
  3. 默认值可通过环境变量配置。
- **涉及文件**：`internal/app/app.go:3149`（APITokenAuthMiddleware）、`internal/config/config.go`、`internal/database/database.go:1876`（`CreateAPIToken` 默认 `rate_limit`）。
- **优先级**：P1

#### REQ-MIG-01 · 迁移错误不再静默忽略
- **描述**：`migrate()`（`database.go:174-381`）中大量 `ALTER TABLE` / `CREATE`（约 `database.go:224-338`）直接 `d.db.Exec(...)` 忽略 `err`。改为检查 err，失败则显式返回，由 `initDB`（`database.go:158-172`）→ `NewDatabase`（`database.go:120-142`）透传，导致启动失败（或至少 fatal 日志 + 非 0 退出），而非静默跳过造成 schema 不一致。
- **验收标准**：
  1. `migrate()` 中任一迁移语句失败时返回 error；
  2. 进程启动因迁移失败而退出（非 0），日志含具体失败 SQL；
  3. 补充单测覆盖"迁移失败即报错"行为。
- **涉及文件**：`internal/database/database.go:174-381`（migrate）、`158-172`（initDB）、`120-142`（NewDatabase）。
- **优先级**：P1

#### REQ-BAK-01 · 备份自动清理
- **描述**：当前仅有手动 `AdminPruneOldBackups`（`app.go:3601`）与 `PruneOldBackups`（`database.go:2123`，按天清理），路由 `/prune-backups`（`routes.go:305`），但**每日备份调度器（`cmd/feedshit/main.go:56-67`）只备份不清**。在每日备份后自动调用 `PruneOldBackups`，按保留天数（如 `BACKUP_RETENTION_DAYS=30`）和/或保留数量清理旧备份。
- **验收标准**：
  1. 每日备份（`main.go:56-67`）执行后自动触发清理；
  2. 超过保留策略的旧备份被删除，磁盘不再无限增长；
  3. 保留策略可通过环境变量配置。
- **涉及文件**：`cmd/feedshit/main.go:48-67`（调度器）、`internal/database/database.go:2123`（PruneOldBackups）、`internal/config/config.go`。
- **优先级**：P1

#### REQ-IMG-01 · 镜像打不可变 tag
- **描述**：`docker-compose.yml:3` 为 `build: .` 无 `image` tag，`Dockerfile` 未打 tag（默认 latest）。改为构建时打语义化版本 / commit sha 的不可变 tag，compose 引用带 tag 镜像，避免裸推 `latest` 造成版本不可追溯。
- **验收标准**：
  1. `docker-compose.yml` 使用带明确 tag 的镜像（非 `latest` 裸推）；
  2. tag 来源可追溯（版本号或 commit sha），CI 构建时注入。
- **涉及文件**：`docker-compose.yml`、`Dockerfile`、CI 配置（见 REQ-CI-01）。
- **优先级**：P1

#### REQ-ENV-01 · 提供 `.env.example` 模板
- **描述**：新增 `.env.example`，列出全部环境变量（参考 `docker-compose.yml:10-28` 与 `README.md` 配置表），含安全相关项（`ADMIN_PASSWORD`、`SMTP_PASS`、`WEBHOOK_URL`，以及若采用加密方案的 `FEEDSHIT_MASTER_KEY` 占位），并注明**不提交真实值、不提交弱口令 `changeme`**。
- **验收标准**：
  1. `.env.example` 存在且覆盖所有 env（含端口 / 数据目录 / SMTP / Webhook / 限速 / 密钥）；
  2. 文件含注释说明敏感变量不入库、生产必须修改默认值。
- **涉及文件**：`.env.example`（新增）、`docker-compose.yml`、`README.md`（引用）。
- **优先级**：P1

---

## 4. 待确认问题（Open Questions）

| # | 问题 | 选项 / 建议 | 影响范围 | 现状证据 |
|---|------|-------------|----------|----------|
| (a) | **密钥加密方案**选哪种？ | A. AES-GCM + 主密钥 `FEEDSHIT_MASTER_KEY`（仅 env）加密落库；B. 完全不落库、仅环境变量（DB 不再存凭据）；C. 其他（如外部密钥管理）。**建议 P0 优先 B，若必须后台可配则 A**。 | REQ-SECRET-01/02/03、REQ-ENV-01 | `email.go:37`、`database.go:775`、`config.go:41/49`、`database.go:357` |
| (b) | **legacy webhook**加签名还是下线？ | A. 配 legacy secret 走 HMAC（复用 `deliverWebhook` 签名逻辑）；B. 下线 legacy 分支、强推订阅式。**建议 B 更干净，但需确认是否有外部系统依赖全局 `webhook_url`**。 | REQ-WH-01 | `app.go:1668-1673`、`database.go:2012` |
| (c) | **CI 平台**默认？ | A. GitHub Actions（项目在 `github.com/Asunano/FeedShit`）；B. GitLab CI / 其他。 | REQ-CI-01、REQ-IMG-01 | 无现有 `.github/` |
| (d) | **单元测试范围**？ | A. 仅安全关键纯函数（REQ-TEST-01 列表）；B. 扩展到 RBAC 权限计算（`GetEffectiveRole` `database.go:1675`、`GetAdminAccessPlan` `database.go:2185`）与轻量集成。 | REQ-TEST-01 | `architecture-review` §3.3 |
| (e) | **默认限速取值**？ | 外部 API token 默认 `rate_limit` 建议值（如 60/小时）与保留天数 `BACKUP_RETENTION_DAYS`（如 30）。 | REQ-RL-01、REQ-BAK-01 | `app.go:3162`、`database.go:2123` |

> 注：(a)(b) 为阻塞项，需在工程启动前拍板；(c)(d)(e) 可在实施中由架构师按默认值推进，但建议先确认以免返工。

---

## 5. 不做的事（明确排除）

- 不做 app.go / database.go 的 god-object 拆分（属阶段4 长期债，按绞杀者模式渐进）。
- 不新增业务功能（M1/M5/M8 等属阶段2/3）。
- 不引入新的外部依赖（保持零外部依赖基调，测试用标准库 `testing`）。
- 不涉及任何前端/UI 界面变更（本批纯后端 + 运维加固）。
