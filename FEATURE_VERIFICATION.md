# 分类 + 细粒度权限 功能验收报告（仅检查，未修改代码）

> 结论：`go build ./...` 与 `go vet ./...` 均退出码 0，代码可编译、无静态错误。
> 数据模型、SQL 下推、写权限、授权 UI 均已正确实现；但存在 **3 个高严重度缺口**（读取越权 / 无授权=全可见 / 提交不校验分类）和若干中低问题，需修复后方可安全上线。

---

## 一、实现正确的部分（✅）

| 项目 | 位置 | 说明 |
|---|---|---|
| 数据模型 | `database.go:229-230, 288-331` | `feedbacks.category` 列 + 迁移；`categories`、`member_grants` 表；迁移用 `INSERT OR IGNORE` 幂等，正确 |
| 分类 CRUD | `database.go:1607-1700` + `app.go:3183-3291` | 增删改查、查重、审计日志齐全；handler 正确用 `GetProject(id)` 解析 slug |
| 授权 CRUD | `database.go:1475-1545` + `app.go:2275-2380` | 列表/批量设置/单删；角色白名单校验；审计日志齐全 |
| 有效角色判定 | `database.go:1549-1575` | `GetEffectiveRole` 优先级正确：精确 (project,cat) > 通配 `*`，取最高角色 |
| SQL 下推（核心） | `database.go:362-411, 427-438, 479-497` | `buildAccessPlanWhere` 把"项目+分类"限制下推到 `ListFeedbacks`/`SearchFeedbacks` 的 `WHERE` 与 `COUNT`；参数占位符顺序正确（先分类后 project_id） |
| 写权限 | `app.go:134-154` | `checkFeedbackWritePerm` 用 `GetEffectiveRole` 并要求 level≥2（editor），状态/回复/改分类均走此校验 |
| 授权 UI | `admin.html:1963-2052` | 团队管理渲染"项目 × 角色 × 分类"矩阵并 PUT 保存 |

---

## 二、高严重度缺陷（🔴 必须修复）

### H1｜单条反馈查看无权限校验（读取越权）
`AdminGetFeedback`（`app.go:626-639`）直接 `GetFeedback(id)` 后返回，**完全不校验项目/分类授权**。
后果：任何已登录的非超管（含被分类限制的成员）都能通过 ID 直接读取其无权访问的反馈。
修复：在返回前调用访问计划校验（参考 `checkFeedbackWritePerm` 的读变体：`GetEffectiveRole(project,cat)==""` 即拒绝）。

### H2｜"无授权 = 看得见全部"（新账号提权）
`AdminListFeedbacks`（`app.go:535-594`）**仅当 `plan != nil` 时才做限制**。
- `GetAdminAccessPlan` 无授权时返回 `nil`（空切片）。
- `AdminCreateAdmin`（`app.go:2116-2162`）只调 `CreateAdmin`，**不写任何 `member_grant`**。
后果：新建的 editor/viewer 账号 `plan==nil` → 列表查询 `ListFeedbacks(nil,nil)` 返回**全部项目全部反馈**。而写操作因 `GetEffectiveRole` 返回 `""` 被拒 → "能看不能改"的越权不一致。
代码注释把"无授权=全可见"当作向后兼容默认，但这与细粒度 RBAC 目标冲突。
修复：非超管且 `plan` 为空（或空计划）时，列表/搜索返回空结果（无访问 = 无数据），并要求显式授权。

### H3｜提交端不校验分类字典（数据污染 + 可见性错乱）
- `SubmitFeedback`（`app.go:256`）把 `c.PostForm("category")` 直接入库，未校验该 key 是否存在于项目 `categories` 字典、是否启用。
- `SubmitFeedbackWithToken`（`app.go:2685`）同样未校验 `req.Category`。
后果：
- 任意字符串污染受控词表；
- 带未知分类的反馈对"分类受限"管理员**不可见**（其 `AllowedCategories` 不含该脏 key），统计也出现脏分类；
- 破坏"按性能/界面/网络收集"的核心目标。
修复：提交时 `GetCategoryByKey(project, category)`，不存在/停用则拒绝或回退 `""`（并建议公开表单以下拉呈现字典）。

---

## 三、中低严重度问题

| 级别 | 位置 | 问题 |
|---|---|---|
| 中 | `routes.go:239-240` + `app.go:3096-` | `project_members` 表及 `AdminGet/SetProjectMembers` 仍是旧体系，访问已改由 `member_grants` 驱动；两者并存易混淆（改 project_members 不再生效）。建议移除或双向同步 |
| 中 | `app.go:3278` `DeleteCategory` | 硬删分类后，原 `category` 字段的反馈变成悬空值（分类受限管理员看不到、统计出现孤儿项）。建议：删除前检查引用，或迁回 `""`/默认分类 |
| 低 | `app.go:3308` `AdminUpdateFeedbackCategory` | 用反馈**当前**分类做写校验；改到新分类后，用户可能对该新分类无写权限，导致自己改完却看不到该条 |
| 低 | `admin.html:2008` | 授权矩阵用**自由文本逗号分隔**填写分类 key，易拼错成不存在的分类；`AdminSetMemberGrants` 也未校验 `category_key` 属于该项目字典 |
| 低 | `feedback.html` | 公开表单**无任何分类选择**（0 处匹配），公开提交的分类恒为空；而后端读顶层 `category` 字段，不走 `custom_data`。分类收集需公开表单提供下拉 |

---

## 四、前端完整性核对

| 能力 | 状态 |
|---|---|
| 团队授权矩阵（项目×角色×分类） | ✅ 已接（admin.html:1963-2052） |
| 分类管理（增删改查）后端 API | ✅ 已接且受保护 |
| 反馈列表"按分类筛选"UI | ❌ 列表虽支持 `category` 查询参数（`app.go:516,605-606`），但**前端无筛选控件**发送它 |
| 公开表单分类下拉 | ❌ 缺失（见 H3 / 低-5） |
| 详情页改分类 UI | 需确认（后端 `PATCH /feedbacks/:id/category` 存在，前端是否调用待查） |

---

## 五、修复优先级

1. **H1**：`AdminGetFeedback` 加读取权限校验 — 安全。
2. **H2**：非超管空计划 → 拒绝访问 — 安全/一致性。
3. **H3**：公开 + Token 提交校验分类字典 — 数据正确性 + 可见性。
4. 中：下线/同步 `project_members`；`DeleteCategory` 防孤儿。
5. 低：授权分类用下拉校验；公开表单分类下拉；列表分类筛选 UI。

> 所有路由与 CSRF、防暴破、PoW、参数化 SQL 等既有防护保持不变，未发现回归。
