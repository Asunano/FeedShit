# FeedShit 运维手册（Runbook）

> 适用范围：FeedShit 阶段0 + 阶段1 加固版本。所有运维操作均假设**单实例**部署。
> 镜像使用不可变版本 tag（如 `feedshit:1.2.3`），切勿在生产使用裸 `latest`。

---

## 1. 核心约束：禁止水平扩展（scale > 1）

**FeedShit 是有状态单实例应用，绝对不要运行多个副本（replicas > 1 / scale > 1）。**

原因（均为内存态，多副本会失效或产生错误结果）：

| 状态 | 存储位置 | 多副本后果 |
|------|----------|------------|
| 管理员会话（Session） | 进程内存 `SessionManager` | 副本间不共享，登录态漂移、频繁重新登录 |
| 每 IP 限速计数器 | 进程内存 `RateLimiter` | 限速被分摊，等于事实上的限额放大 |
| 每 Token 限速计数器 | 进程内存 `tokenHourHits` | 同上，限速形同虚设 |
| PoW nonce 重放缓存 | 进程内存 `NonceCache` | 重放防护被绕过 |
| 登录暴力破解锁定 | 进程内存 `LoginAttemptTracker` | 锁定被绕过 |
| Webhook 投递去重/重试 | 进程内存 outbox ticker | 同一事件可能被多个副本重复投递 |

**正确做法**：始终以**单副本（1 个容器 / 1 个进程）**运行。如需提高可用性或吞吐：
- 使用单实例 + 进程守护（systemd / supervisor / Docker restart policy）；
- 数据库文件与 `./data` 目录通过**数据卷**持久化（不要放进容器可写层）；
- 横向扩展请等待官方分布式会话/限速/队列方案，**当前版本不支持**。

---

## 2. 部署

### 2.1 前置条件

- Go 1.26+（本地构建）或 Docker（推荐）。
- 生成主密钥并设置 `FEEDSHIT_MASTER_KEY`（**缺失即启动失败**）：

  ```bash
  export FEEDSHIT_MASTER_KEY=$(head -c 32 /dev/urandom | xxd -p -c 32)
  ```

- 复制并填写环境变量：

  ```bash
  cp .env.example .env
  # 编辑 .env，至少设置 FEEDSHIT_MASTER_KEY、ADMIN_PASSWORD 等
  ```

### 2.2 Docker（推荐）

```bash
docker compose up -d
```

镜像 tag 来自 `TAG` 环境变量（默认 `1.0`）。生产构建请显式指定版本：

```bash
TAG=1.2.3 docker compose up -d --build
# 或
docker build --build-arg VERSION=1.2.3 -t feedshit:1.2.3 .
docker run -d --name feedshit -p 8080:8080 -v $(pwd)/data:/app/data --env-file .env feedshit:1.2.3
```

### 2.3 本地构建

```bash
go build -o feedshit ./cmd/feedshit/
FEEDSHIT_MASTER_KEY=... ./feedshit
```

---

## 3. 数据目录与 WAL

数据目录（默认 `./data`，容器内 `/app/data`）包含：

| 文件 / 目录 | 说明 |
|-------------|------|
| `feedbacks.db` | 主 SQLite 数据库 |
| `feedbacks.db-wal` / `feedbacks.db-shm` | WAL 预写日志（崩溃恢复用） |
| `uploads/` | 用户上传的附件 |
| `backups/` | 每日自动备份（`feedbacks_YYYYMMDD_HHMMSS.db`） |

**WAL 模式说明**：数据库以 WAL 模式打开，提供崩溃一致性。备份使用 SQLite 原生的
`VACUUM INTO`（一致性快照），因此备份期间无需停服。请勿手动删除 `-wal`/`-shm`
文件，否则可能丢失最近提交。

**务必挂载数据卷**：Docker 部署已配置 `./data:/app/data`；备份与升级都应基于此卷。

---

## 4. 备份与恢复

### 4.1 自动备份

- 进程启动时立即做一次备份；
- 每日 03:00（宿主机时区）定时备份；
- 每次备份后按 `BACKUP_RETENTION_DAYS`（默认 30）自动清理超过保留期的旧备份。

### 4.2 手动备份

- 后台「数据」→「备份」按钮触发一次性备份；
- 或手动复制数据库文件（先 `sqlite3 feedbacks.db "PRAGMA wal_checkpoint(TRUNCATE)"` 合并 WAL 再拷）。

### 4.3 恢复

1. 停止 FeedShit 进程（单实例，停一个即可）；
2. 用备份文件覆盖 `./data/feedbacks.db`（同时保留/覆盖 `-wal`/`-shm` 或先清空二者）；
3. 确保 `FEEDSHIT_MASTER_KEY` 与备份时**一致**——SMTP 密码、Webhook secret 均经该密钥
   AES-GCM 加密，密钥不符将无法解密（邮件/Webhook 通知失效）；
4. 重新启动进程，验证 `/health`。

---

## 5. 升级与回滚

### 5.1 升级

1. **先备份**（见 4.1 / 4.2）；
2. 拉取新版本镜像（不可变 tag）或重新构建二进制；
3. 停止旧进程，启动新进程；
4. 启动时自动执行 Schema 迁移（幂等，重复列/已存在对象会被安全忽略）；
5. 验证 `/health` 与后台页面正常。

> Schema 迁移在启动期 fail-fast：若出现不可忽略的迁移错误，进程会以非 0 退出并
> 在日志打印具体失败 SQL。此时**不要反复重启**，应先排查数据库状态或从备份恢复。

### 5.2 回滚

1. 停止当前进程；
2. 用升级前的备份恢复数据库（见 4.3）；
3. 启动旧版本镜像/二进制；
4. 验证服务。

> 注意：如果新版本迁移**新增了列/表**，回滚到旧版本后这些结构会被忽略（向后兼容）。
> 若新版本**删除了**结构，则无法直接回滚，须依赖升级前备份。

---

## 6. 密钥与凭据

- `FEEDSHIT_MASTER_KEY`：AES-GCM 主密钥，**仅环境变量**。32 字节原始值或 64 位十六进制。
  缺失或长度错误 → 启动 `log.Fatalf` 退出（fail-fast）。
- SMTP 密码、Webhook 订阅 secret：以 `aes-gcm:<base64>` 密文落库；读取时解密。
  DB 导出/备份**不含明文**。
- API Token（`fs_...`）：高熵随机令牌，按值查找，**不加密**（加密会破坏查询）；
  请像密码一样妥善保管，泄露后及时在后台吊销。

---

## 7. 健康检查与监控

- `GET /health` 返回 DB 连通性，适合作为容器存活/就绪探针。
- 关键日志关键字：`[WEBHOOK]`（Webhook 投递）、`[MAIL]`（邮件发送）、
  `Daily backup` / `Pruned N old backup(s)`（备份清理）。

---

## 8. 常见问题

| 现象 | 可能原因 | 处理 |
|------|----------|------|
| 启动即退出，日志 `Failed to initialize security (master key)` | 未设置 `FEEDSHIT_MASTER_KEY` 或长度不对 | 设置 32 字节/64 十六进制主密钥 |
| 邮件/Webhook 通知失效 | 主密钥与历史数据不一致 | 使用与备份时相同的主密钥 |
| 启动退出，日志 `migration failed` | Schema 迁移失败（非幂等错误） | 检查 DB；必要时从备份恢复后重试 |
| 多副本下登录态/限速异常 | 违反单实例约束 | 缩容到 1 副本 |
| 磁盘持续增长 | 备份未清理 | 确认 `BACKUP_RETENTION_DAYS>0` |
