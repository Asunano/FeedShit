# FeedShit 待补充功能推荐（含用途说明与实现思路）

> 仅设计建议，未修改代码。本文只保留**尚未实现**的功能，已实现/已修复内容不再列出。
> 每项按「它解决什么问题 → 用户/管理者从哪里感受到 → 怎么实现」组织，便于判断是否值得做。

---

## 一、仍待处理的中低项（技术债，非阻塞）

| 项 | 用处 | 位置 | 建议做法 |
|---|---|---|---|
| 列表分类筛选 UI | 后端已支持按分类查询，但管理员在列表页没有下拉可选，只能看全部——加上后能"只看性能类/只看网络类" | `admin.html` 过滤栏 | 加一个分类下拉，与状态/优先级并列 |
| `DeleteCategory` 孤儿反馈 | 删分类后，原来属于它的反馈分类字段悬空，统计和筛选会出错 | 分类删除逻辑 | 删前校验引用，或把相关反馈迁回默认分类；改软删 `is_active` |
| 旧 `project_members` 端点 | 权限已改由 `member_grants` 驱动，旧端点还在会造成"两套数据源"混淆，埋权限 bug | `routes.go` | 下线旧端点 |
| 状态词表硬编码 | 状态写死 `pending/processing/resolved/closed`，不同团队/项目无法用自己的流程 | 前后端 | 见 M8 自定义状态工作流 |
| 新建管理员空授权 | 新账号建好后没有任何授权，登录进来什么都看不到，体验差 | `AdminCreateAdmin` | 创建对话框内一并设授权 |

---

## 二、待补充的新功能（按用途 + 实现思路）

### M1 自动分派 + SLA 升级
**解决什么问题**：新反馈进来后"谁来处理""多久必须处理"全靠人工盯。规模一大就有反馈被漏、被拖。
**用户/管理者从哪里感受到**：
- 反馈一提交，系统按规则自动填好处理人（`assignee`），管理员打开列表就看到"已分派给谁"，不用手动分拣。
- 每条反馈显示 SLA 倒计时（如"距首次响应还剩 1h20m"，红/黄/绿）；超时自动通知 manager 催办。
**实现思路**：
- **数据模型**
  ```sql
  CREATE TABLE automation_rules (
    id INTEGER PRIMARY KEY, project_slug TEXT, category_key TEXT, -- '*' 全分类
    priority TEXT, action TEXT,           -- 'assign' | 'escalate'
    target_admin_id INTEGER, sla_first_resp_min INTEGER, sla_resolve_min INTEGER
  );
  CREATE TABLE feedback_sla (
    feedback_id INTEGER PRIMARY KEY, first_resp_due_at TIMESTAMP,
    resolve_due_at TIMESTAMP, escalated INTEGER DEFAULT 0
  );
  ```
- **后端**：`InsertFeedback` 后调 `ApplyAutomation(fb)` 命中最优先规则→写 `assignee` 与 `due_at`；新增 goroutine ticker 定期扫超时未达标行，标记 `escalated=1` 并复用 `sendWebhookEvent("sla_escalate",...)` 通知。
- **前端**：项目设置加规则列表；列表加 SLA 倒计时列。
- **复杂度**：中（难点在多实例去重）。

### M2 提交者满意度评分（CSAT）
**解决什么问题**：现在只能知道反馈"关闭了"，不知道用户是否真的满意，无法衡量处理质量。
**用户/管理者从哪里感受到**：
- 提交者侧：反馈变"已解决"后，`track.html` 进度页出现 1–5 星评分（或邮件带评分链接）。
- 管理者侧：仪表盘显示平均满意度、按处理人/分类的满意度分布——看出"谁处理得好、哪类问题用户最不满"。
**实现思路**：
- **数据模型**：`feedback_ratings(feedback_id PK, score 1..5, comment, created_at)`。
- **后端**：状态变 `resolved` 时复用邮件通道发 CSAT 邀请（含 `tracking_token`）；`POST /track/:token/rating` 写评分。
- **前端**：`track.html` 星级；仪表盘平均分卡片。
- **复杂度**：低—中。

### M3 公开 Roadmap 看板
**解决什么问题**：提交者提完反馈就"失联"，不知道有没有被采纳，容易觉得"提了也白提"。
**显示什么数据 / 从哪里感受到**：
- 一个**无需登录**的公开页 `roadmap.html`，只展示被标记为公开的反馈；
- 按状态分看板列：**规划中 / 进行中 / 已发布**；卡片显示标题、分类、可选投票数；
- 不显示内部备注、IP、联系方式等敏感信息。
- 作用：提升透明度与信任，鼓励更多有效反馈（类似产品"更新日志/路线图"页）。
**实现思路**：
- **数据模型**：`feedbacks.public_on_roadmap INTEGER DEFAULT 0`（或项目级开关）。
- **后端**：`GET /p/:projectSlug/roadmap` 公开端点，按分类/状态聚合。
- **前端**：新增 `roadmap.html` 看板。
- **复杂度**：低。

### M4 反馈投票 / 热度
**解决什么问题**：无法判断哪些反馈是"很多人都想要的"，团队难以排优先级。
**从哪里感受到**：反馈卡片出现 👍 按钮 + 计数；管理列表可按票数排序，一眼看出"呼声最高"的需求。
**实现思路**：
- **数据模型**：`feedback_votes(feedback_id, voter_key, PK(feedback_id,voter_key))`。
- **后端**：`POST /feedback/:id/vote`（匿名用 token/IP 去重，登录用 admin_id）；列表加 `ORDER BY votes DESC`。
- **前端**：卡片投票按钮。
- **复杂度**：低。

### M5 相似反馈自动去重
**解决什么问题**：同一个问题被多人重复提交，列表噪声大、统计失真（当前只有"手动"标记重复）。
**从哪里感受到**：提交时/列表页出现"疑似重复：#123"横幅，管理员一键合并；重复被折叠，列表更清爽。
**实现思路**：
- **本地方案**：`InsertFeedback` 前置 `FindSimilar(fb)`，用 SimHash/MinHash 对标题+描述算指纹存 `feedbacks.hash`，查同项目 `hamming<=3` 的近期反馈。合并复用现有 `is_duplicate`/`duplicate_of`。
- **LLM 升级**：嵌入余弦相似度，更准但有成本。
- **复杂度**：中（本地）/ 中高（LLM）。

### M6 出站 Webhook 签名 + 重试队列
**解决什么问题**：现有 Webhook 是"发出去就不管"，接收方无法验真、失败也不会重发，通知可能丢。
**从哪里感受到**：接收系统能用签名校验消息真伪；网络抖动时通知不再丢失（自动重试）；可按项目/事件订阅不同频道。
**实现思路**：
- 出站加 `X-FeedShit-Signature: HMAC-SHA256(secret, body)`；失败入 `webhook_outbox(id,url,payload,attempts,next_at)`，后台 ticker 指数退避重试；新增 `webhook_subscriptions(project_slug,url,secret,events)` 支持按事件订阅。
- **复杂度**：中。

### M7 API Token 限流 / 配额
**解决什么问题**：当前限流按 IP，API Token（给 CI/监控系统用）没有独立限额，容易被单个 token 刷爆。
**从哪里感受到**：每个 token 有独立速率/日配额，超限返回 429，保护系统稳定。
**实现思路**：把限流改为按 `api_token` 维度，或 `admin_api_tokens` 加 `rate_limit`/`quota_per_day` 列 + 计数器。
- **复杂度**：低。

### M8 自定义状态工作流
**解决什么问题**：状态写死四种，不同团队想要自己的流程（如"待评审→开发中→测试中→上线"）做不到。
**从哪里感受到**：每个项目可自定义状态名称、颜色、允许的流转路径；状态下拉由项目配置动态生成。
**实现思路**：
- **数据模型**
  ```sql
  CREATE TABLE workflows (
    project_slug TEXT, status_key TEXT, label TEXT, color TEXT,
    is_initial INTEGER, is_terminal INTEGER, sort_order INTEGER
  );
  CREATE TABLE workflow_transitions (
    project_slug TEXT, from_status TEXT, to_status TEXT, required_role TEXT
  );
  ```
- **后端**：状态变更走 `CanTransition(project, from, to, role)` 校验；下拉由 API 动态下发。
- **复杂度**：高（状态机重构 + 迁移旧数据），建议单独迭代。

### M9 知识库 / FAQ 自助
**解决什么问题**：很多提交是重复的常见问题，占用处理人力。
**从哪里感受到**：提交者在填描述时，页面实时提示"你可能想看：XXX"，命中就自助解决，无需提交——减少无效反馈量。
**实现思路**：
- **数据模型**：`faqs(project_slug, question, answer, embedding)`。
- **后端**：`GET /faq?q=` 先 LIKE 粗检索（后升级向量）；提交页实时调用。
- **复杂度**：中。

### M10 AI 智能分类 / 自动填充
**解决什么问题**：提交者常选错分类或不选，导致分类不准、统计和权限分派失效。
**从哪里感受到**：提交时系统根据描述自动**建议**分类和优先级并预选，用户可改；管理端收到的反馈分类更准。
**实现思路**：提交端调 `SuggestCategory(desc)`，把项目 `categories` 作为候选传给 LLM，返回 `{category_key, priority, reason}`；无 `LLM_API_KEY` 时降级关键词启发式。
- **复杂度**：中（外部 API，注意成本/超时）。

### M11 SSO / OAuth 管理员登录
**解决什么问题**：管理员要单独记一套账号密码，企业内不便统一管理。
**从哪里感受到**：管理员用 GitHub/Google/企业 OIDC 一键登录，免密码；离职关闭统一身份即失效。
**实现思路**：新增 `internal/auth`，`/admin/login/:provider` 回调后 `upsert admins(sub=...)`，角色回退 `member_grants`；会话沿用现有 Cookie。需配置 `OAUTH_CLIENT_ID/SECRET`。
- **复杂度**：中。

### M12 导出增强 + 每日自动备份调度
**解决什么问题**：
- 当前导出仅 CSV，做数据分析/归档常需要 JSON/Excel。
- `admin.html` 提示"每日凌晨 3 点自动备份"，但 `app.go` 里只有手动备份和 15 分钟级清理 ticker，**未见每日定时调度**——提示与实际可能不符。
**从哪里感受到**：可按当前筛选一键导出 JSON/Excel；数据库每天自动备份，无需人工点击。
**实现思路**：
- 导出：`GET /admin/feedbacks/export?fmt=json|xlsx` 复用 `buildAccessPlanWhere` 保证权限；Excel 用 `excelize`。审计日志加导出。
- 自动备份：启动时加 `time.NewTicker(24h)`（或到点触发）调 `BackupDatabase`。
- **复杂度**：低。

### M13 每周周报邮件
**解决什么问题**：管理者需要定期回顾"这周收了多少、哪类最多、处理得怎么样"，手动统计费时。
**从哪里感受到**：每周自动收到 HTML 周报邮件——分类分布、新增/解决数、SLA 达成率、超时催办清单。
**实现思路**：后台 ticker 每周调 `GetCategoryCounts`/`GetProjectStats` 生成 HTML 发 manager（复用现有 `Mailer`）。
- **复杂度**：低。

### M14 界面体验：暗黑模式 / 响应式 / 快捷键
**解决什么问题**：管理端仅桌面浅色，长时间使用/移动端处理体验一般。
**从哪里感受到**：可切换暗黑模式；手机也能查看处理反馈；列表支持 `j/k` 导航、`e` 编辑等快捷键，处理效率更高。
**实现思路**：CSS 变量 + `prefers-color-scheme`；窄屏适配；键盘事件绑定。
- **复杂度**：低。

---

## 三、落地优先级（quick win → 大改）

| 优先级 | 特性 | 价值定位 | 预估 |
|---|---|---|---|
| 1 | 列表分类筛选 UI | 补齐已有后端能力 | 0.5d |
| 2 | M12 导出增强 + 自动备份调度 | 修正"提示≠实际"，补数据安全 | 1d |
| 3 | M7 API Token 限流 | 系统稳定性 | 0.5d |
| 4 | M2 CSAT 评分 | 质量闭环 | 1–2d |
| 5 | M4 投票/热度 | 需求优先级排序 | 1d |
| 6 | M3 公开 Roadmap | 透明度/用户信任 | 1–2d |
| 7 | M6 Webhook 签名+重试 | 通知可靠性 | 2d |
| 8 | M5 自动去重 | 降噪 | 2–3d |
| 9 | M9 知识库 FAQ | 减少无效提交 | 2–3d |
| 10 | M10 AI 智能分类 | 分类准确率 | 2d |
| 11 | M1 自动分派 + SLA | 处理效率/可追责 | 3d |
| 12 | M13 周报邮件 | 管理回顾 | 1d |
| 13 | M11 SSO | 企业身份统一 | 3d |
| 14 | M14 暗黑模式/响应式 | 使用体验 | 1–2d |
| 15 | M8 自定义状态工作流 | 流程灵活性（架构级） | 5d+ |

---

## 四、通用实施建议
- **权限一致性**：新读/写端点必须复用 `GetEffectiveRole` / `buildAccessPlanWhere` / `checkFeedbackWritePerm`，避免越权复发。
- **复用现有积木**：附件(`file_paths`+`saveUpload`)、Webhook(`sendWebhookEvent`/`build*Card`)、邮件(`Mailer`)、`tracking_token`、`audit_logs`、`categories`、`member_grants`、`RateLimiter` 都是现成设施，拼装而非重造。
- **迁移安全**：`ALTER TABLE` 前先用 `BackupDatabase` 备份；`CREATE TABLE IF NOT EXISTS` 保持幂等。
- **先核对再动手**：动工前 `search_content` 确认代码是否已有实现，避免重复造轮子。
