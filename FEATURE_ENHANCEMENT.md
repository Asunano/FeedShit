# FeedShit 功能完整性评估与增强方案（分类 + 细粒度权限）

> 仅设计与建议，未修改任何代码。聚焦两大缺口：**反馈分类（问题类型）** 与 **团队权限的项目/分类细粒度**。

---

## 一、当前能力盘点（基于代码事实）

### 1. 反馈的"分类"现状
| 机制 | 位置 | 能力 | 缺陷 |
|---|---|---|---|
| `feedbacks.tags` | `database.go:27,161` | 自由文本字符串（逗号分隔） | 无预定义词表；拼写不统一；不能做强约束、统计口径混乱 |
| `feedbacks.custom_data` | `database.go:23` | 存项目 `form_schema` 里的动态字段（可含 select "问题类型"） | 存 JSON，**DB 层无法过滤/聚合**；每个项目各自定义，跨项目不可比；不能做权限维度 |
| `priority` | `database.go:32` | 优先级 | 与"问题类型"是两个维度，不能替代 |

**结论**：目前无一等公民的 `category`（问题类型）字段。要按"性能/界面/网络"分类收集、筛选、统计、分派，只能靠自由 `tags` 或每项目自定义表单字段——**都无法在数据库层过滤、无法做统一统计、无法作为权限边界**。用户判断正确。

### 2. 团队权限现状
| 维度 | 现状 | 位置 |
|---|---|---|
| 角色 | 全局单一角色 `admin`/`editor`/`viewer` | `admins.role` (`database.go:44,233`) |
| 项目可见范围 | `project_members(admin_id, project_slug)` 控制"能看哪些项目" | `database.go:257`；下推查询 `app.go:498-503` |
| 项目内角色 | **无**（一个人在所有可见项目里角色相同） | — |
| 分类级权限 | **无** | — |

**结论**：只有"能看哪些项目"这一层，角色是全局的。无法表达"张三在 A 项目是编辑、在 B 项目只读"，更无法表达"李四只处理 A 项目的性能类反馈"。用户判断正确。

---

## 二、增强方案总览

分两个独立可交付的特性，可分期实施：

- **特性 A：反馈分类（Category / 问题类型）** —— 一等公民字段 + 项目级分类字典 + 筛选/统计/分派。
- **特性 B：细粒度 RBAC** —— 权限从"全局角色 + 项目可见"升级为"(用户 × 项目 × 分类) → 角色"三元组。

设计遵循全栈规范：DB 迁移可逆、访问控制下推 SQL（不在内存过滤）、输入边界校验、错误分类返回。

---

## 三、特性 A：反馈分类（问题类型）

### 3.1 数据模型
新增分类字典表（**按项目**定义，支持"性能/界面/网络/功能/其他"等），并给反馈加一等字段。

```sql
-- 分类字典（每个项目一套，可增删改，带排序与颜色）
CREATE TABLE IF NOT EXISTS categories (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    project_slug TEXT    NOT NULL,
    key          TEXT    NOT NULL,           -- 稳定标识：performance/ui/network...
    name         TEXT    NOT NULL,           -- 展示名：性能问题
    color        TEXT    NOT NULL DEFAULT '',-- 徽章颜色 #f00
    sort_order   INTEGER NOT NULL DEFAULT 0,
    is_active    INTEGER NOT NULL DEFAULT 1,
    UNIQUE(project_slug, key)
);
CREATE INDEX IF NOT EXISTS idx_categories_project ON categories(project_slug);

-- 反馈表新增分类列（迁移用 ALTER，默认空）
ALTER TABLE feedbacks ADD COLUMN category TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_feedbacks_category ON feedbacks(project_id, category);
```

> 迁移写法与现有 `updated_at` 一致：在 `migrate()` 里用 `ALTER TABLE ... ADD COLUMN`（先探测列是否存在再加，避免重复报错）。

**为什么用独立 `category` 列而不是复用 `tags`/`custom_data`：**
- `category` 单选、受控词表 → 可建索引、可 `GROUP BY` 统计、可作权限边界；
- `tags` 保留为多值自由标签（并存，互补）；
- `custom_data` 继续放项目特有的杂项字段。

### 3.2 提交端
- 公开表单：分类可作为一种新的 `form_schema` 字段类型 `category`（渲染成下拉，选项来自该项目 `categories`），或独立必填下拉。提交时写入 `feedbacks.category`。
- 也支持"智能预分类"：根据关键词/正则把标题+描述映射到默认分类（可选增强）。

### 3.3 后台
- 列表页新增 **按分类筛选** 的 facet（下推 SQL：`WHERE category = ?`），与现有状态/项目筛选并列。
- 详情页可改分类（记审计日志）。
- 批量操作：批量改分类。

### 3.4 统计
- 新增"分类分布"图（`SELECT category, COUNT(*) ... GROUP BY category`）；
- 按分类的平均响应时长、未处理积压 Top 分类。

### 3.5 API 增量
```
GET    /api/v1/admin/projects/:slug/categories       列出分类
POST   /api/v1/admin/projects/:slug/categories       新建
PUT    /api/v1/admin/categories/:id                   改名/颜色/排序/启用
DELETE /api/v1/admin/categories/:id                   删除（软删或校验无引用）
PATCH  /api/v1/admin/feedbacks/:id/category           改某条反馈分类
GET    /api/v1/feedback/form/:slug                     表单 schema 追加 categories 选项
```

---

## 四、特性 B：细粒度权限（项目 × 分类 × 角色）

### 4.1 目标语义
- 张三：项目 A = 编辑，项目 B = 只读；
- 李四：仅项目 A、且仅"性能"与"网络"两个分类 = 编辑；其他分类不可见；
- 保留全局超管（`admins.role='admin'` 无限制）。

### 4.2 数据模型（升级 `project_members`）
把"可见项目"表升级为"授权三元组"表。分类维度用 `category_key`，`*` 或空表示"该项目全部分类"。

```sql
-- 新表：成员在某项目某分类下的角色
CREATE TABLE IF NOT EXISTS member_grants (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    admin_id      INTEGER NOT NULL,
    project_slug  TEXT    NOT NULL,
    category_key  TEXT    NOT NULL DEFAULT '*',  -- '*' = 该项目所有分类
    role          TEXT    NOT NULL DEFAULT 'viewer', -- viewer/editor/manager
    UNIQUE(admin_id, project_slug, category_key)
);
CREATE INDEX IF NOT EXISTS idx_grants_admin ON member_grants(admin_id);
```

**迁移策略（兼容现有数据）**：
- 保留 `project_members` 一段时间，或一次性迁移：
  `INSERT INTO member_grants(admin_id, project_slug, category_key, role) SELECT admin_id, project_slug, '*', <该 admin 的全局 role> FROM project_members;`
- `admins.role` 保留为"账号默认角色/是否超管"；具体项目角色以 `member_grants` 为准。

### 4.3 权限判定（下推 SQL，不在内存过滤）
定义两个辅助查询：
- 可见项目集：`SELECT DISTINCT project_slug FROM member_grants WHERE admin_id=?`
- 某项目内可见分类集：`SELECT category_key FROM member_grants WHERE admin_id=? AND project_slug=?`（含 `*` 则全部）

反馈列表 `WHERE` 组装示例（受限用户）：
```sql
WHERE (
  (project_id IN (<有 '*' 授权的项目>))
  OR
  (project_id = ? AND category IN (?,?,...))   -- 逐个受限项目的分类白名单
  OR ...
)
```
写操作（改状态/回复/改分类/删除）在 handler 层校验：该用户在 `(该反馈.project, 该反馈.category)` 上的有效角色 ≥ `editor`。

**有效角色求解**：优先精确匹配 `(project, category)`，否则回退 `(project, '*')`；取其中较高角色。`manager` 可管理该项目成员授权。

### 4.4 角色能力矩阵（建议）
| 能力 | viewer | editor | manager | admin(超管) |
|---|---|---|---|---|
| 查看反馈 | 授权范围内 | 授权范围内 | 项目内全部 | 全部 |
| 改状态/回复/分类/指派 | ✗ | ✓（授权范围） | ✓（项目内） | ✓ |
| 导出/批量 | ✗ | ✓ | ✓ | ✓ |
| 管理分类字典 | ✗ | ✗ | ✓（本项目） | ✓ |
| 管理成员授权 | ✗ | ✗ | ✓（本项目） | ✓ |
| 项目/全局配置/备份 | ✗ | ✗ | ✗ | ✓ |

### 4.5 API 增量
```
GET    /api/v1/admin/admins/:id/grants                查某成员的全部授权
PUT    /api/v1/admin/admins/:id/grants                批量设置授权 [{project,category,role}]
DELETE /api/v1/admin/admins/:id/grants/:grantId       撤销单条
```
后台"团队管理"页从"勾选项目"升级为"项目 + 分类 + 角色"的三级授权矩阵 UI。

---

## 五、对现有缺陷的联动修复
- 特性 B 天然要求把访问控制**全部下推 SQL**，正好巩固此前"多项目翻页 total 错误"的修复思路（`app.go:474`）。
- 分类改动、授权改动都应写 `audit_logs`（已有表），形成完整操作审计。
- 外部 token 提交（`/feedback/submit/:token`）也应接受并校验 `category` 属于该项目字典，避免脏分类。

---

## 六、实施优先级与工作量（估算）
| 阶段 | 内容 | 依赖 | 规模 |
|---|---|---|---|
| A1 | `categories` 表 + `feedbacks.category` 迁移 + CRUD API | 无 | 小 |
| A2 | 提交端分类字段 + 后台筛选/详情改分类 | A1 | 中 |
| A3 | 分类统计图表 | A1 | 小 |
| B1 | `member_grants` 表 + 迁移脚本（兼容 `project_members`） | 建议在 A1 后 | 中 |
| B2 | 权限判定下推 SQL（列表/详情/写操作） | B1 | 中—大 |
| B3 | 团队授权矩阵 UI + manager 角色 | B1、B2 | 中 |

**推荐顺序**：A1 → A2 → B1 → B2 →（A3 / B3 并行）。先把"分类"落地成一等字段，再让权限挂到分类上，避免返工。

---

## 七、风险与注意点
- **迁移可逆性**：`ALTER TABLE ADD COLUMN` 不可直接回滚，需在迁移前做数据库备份（备份功能已修复）。
- **分类删除**：删除有引用的分类应软删（`is_active=0`）或迁移历史反馈到"其他"，避免出现悬空 `category`。
- **权限回退兼容**：上线 B 特性时，务必先把旧 `project_members` 迁移进 `member_grants`，否则现有受限成员会瞬间失去访问。
- **超管兜底**：任何时候至少保留一个 `role='admin'` 账号，权限 UI 需禁止删除最后一个超管。
- **性能**：`feedbacks(project_id, category)` 复合索引已覆盖筛选；成员授权表小，可全量加载到请求上下文缓存。
