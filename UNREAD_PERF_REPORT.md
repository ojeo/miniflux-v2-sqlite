# 批量更新订阅时未读列表卡顿问题分析报告

- 项目：miniflux-v2-sqlite（miniflux 的 SQLite fork）
- 数据库：`f:\Workspace\miniflux\v2\miniflux.db`
- 报告日期：2026-08-25

---

## 1. 问题描述

用户在**批量更新订阅（刷新全部订阅）期间访问未读消息列表**时，观察到页面响应卡顿。

## 2. 结论摘要

**根因不是未读列表查询慢，也不是 WAL 写锁阻塞读，而是连接池被限制为单个连接（`MaxOpenConns=1`），导致并发刷新写者与未读读者在同一个连接上排队。**

- 所有未读查询均已命中索引，基线延迟仅约 `1.7 ms`，查询侧完全健康；
- 实测在 16 个并发刷新写者竞争下，未读读者延迟放大至 `26 ms（p50）/ 100 ms（max）`，放大约 15–23 倍；
- WAL 已正确启用（`journal_mode=WAL`），但本 fork 把连接池钉死在单连接上，使 WAL 的"单写多读"并发能力未能被利用。

## 3. 关键代码事实

### 3.1 连接池被限制为单连接

`internal/database/sqlite.go`：

```go
func NewConnectionPool(dsn string, ...) (*sql.DB, error) {
    sqliteDSN := buildSQLiteDSN(dsn) // journal_mode(WAL)&busy_timeout(30000)&...
    db, err := sql.Open("sqlite", sqliteDSN)
    ...
    db.SetMaxOpenConns(1)
    db.SetMaxIdleConns(1)
    ...
}
```

- DSN 配置：`journal_mode(WAL)`、`busy_timeout(30000)`、`synchronous(NORMAL)`、`foreign_keys(1)`。
- 全部读写（后台刷新写、未读列表读、导航元数据读）共享这**唯一**的 `*sql.DB`。

### 3.2 并发刷新写者规模

- 后台刷新使用 worker 池，`WORKER_POOL_SIZE` 默认 `16`（`internal/config/options.go`、`internal/worker/pool.go`）。
- 16 个 worker 全部共享同一个 `*storage.Storage` → 同一个单连接池。

### 3.3 写事务粒度极小但次数极多

`internal/storage/entry.go` 的 `RefreshFeedEntries`：

```go
for _, entry := range entries {
    tx, err := s.db.Begin()          // 每条 entry 单独开事务
    ...
    entryExists, _ := s.entryExists(tx, entry)
    if entryExists {
        if updateExistingEntries { s.updateEntry(tx, entry) }
    } else {
        s.createEntry(tx, entry)     // 触发 entries_fts AFTER INSERT 触发器
    }
    ...
    tx.Commit()                      // 每条 entry 单独提交
}
```

- 每个 feed 的每条新条目 = 一次"连接获取 + 事务 + FTS 索引维护 + 提交"。
- 多 feed 并行刷新时，写请求源源不断地抢占唯一连接。

### 3.4 未读列表读取路径

`internal/ui/unread_entries.go` 的 `showUnreadPage`：

- `GetEntriesWithCount()`：先 `CountEntries()`（COUNT），再 `fetchEntries()`（数据）。
- 同时调用 `GetNavMetadata()`（未读 COUNT + 错误 feed COUNT + save 标志）。
- 以上全部读操作与刷新写共享同一单连接。

### 3.5 查询索引健康

`schema/migrations` 已建立充分索引，实测 `EXPLAIN QUERY PLAN` 均命中：

| 查询 | 使用的索引 |
|---|---|
| 未读 COUNT | `entries_user_status_feed_idx(user_id, status, feed_id)`（覆盖索引） |
| 未读列表（按 id 排序） | `entries_user_status_idx(user_id, status)` |
| 未读列表（按 published_at 排序） | `entries_user_status_published_idx(user_id, status, published_at)` |

`entries_fts` 为外部内容 FTS 表，由 `entries_ai/entries_au/entries_ad` 触发器同步维护。

## 4. 实测验证

### 4.1 数据库规模（只读）

| 指标 | 值 |
|---|---|
| users | 2 |
| feeds | 29 |
| entries | 13,019 |
| unread entries | 206 |
| journal_mode | `wal` |

在 13,019 行规模下，即使全表扫描也应在毫秒级，未读列表查询本身不可能是瓶颈。

### 4.2 连接竞争实测

复用项目自身的 `database.NewConnectionPool`（含 `MaxOpenConns=1`），读者执行与 UI 相同的未读列表查询；写者模拟 `RefreshFeedEntries` 风格写负载（事务内 INSERT 触发 FTS 触发器，随后回滚，不污染数据）。

**未读读者延迟（毫秒）：**

| 场景 | min | p50 | p95 | max |
|---|---|---|---|---|
| 基线（单连接空闲） | 1.00 | **1.73** | 2.77 | 4.38 |
| 16 并发写者竞争 | 12.07 | **25.97** | 63.83 | 100.16 |

- p50 放大 **15 倍**（1.73 → 25.97 ms）
- p95 放大 **23 倍**（2.77 → 63.83 ms）
- 数据量进一步增大、feed 更多、刷新更频繁时，放大效应会更明显。

**结论：** 批量刷新写者对唯一连接的持续占用，使未读读者延迟显著放大，是卡顿的直接原因。

## 5. 根因分析

1. **连接池 `MaxOpenConns=1`**：单写者模型下，为防止多连接 goroutine 抢锁触发 `SQLITE_BUSY`，本 fork 强制单连接。但这也意味着**读写完全串行**。
2. **高并发写者**：16 个刷新 worker 对每条 entry 做短事务，持续占据连接。
3. **WAL 能力未利用**：WAL 本支持"1 写 + N 读"并发，但单连接把读者也串行到写队列之后。
4. **写事务粒度过小**：每条 entry 一个事务，连接往返次数多，放大了排队效应。

> 注意：本场景并非 WAL 写锁阻塞读，也不是查询语句问题；是**单连接池 + 高并发写负载**导致的读排队。

## 6. 解决方案

### 方案 A：读写双连接（根治，改动大）

- 为只读路径单独建立连接池（`MaxOpenConns=N`），利用 WAL 实现读写并发。
- 读操作（未读列表、条目详情、导航元数据等）走 `readerDB`；写操作走单连接的 `writerDB`。
- 优点：彻底隔离读写，是"单写多读"最正确的建模。
- 风险：需将 `storage.Storage` 拆分为读/写两组方法，改动面大，回归风险高。

### 方案 B：批量写事务（优化写侧，改动中等）

- `RefreshFeedEntries` 改为单 feed 一个事务（或分批提交，如每 200 条一个事务），减少连接往返。
- 优点：降低连接占用频率，间接减小读排队窗口。
- 风险：单 feed 条目多时单事务变长，可能更久占用连接，需权衡分批大小。

### 方案 C：worker 写限流（最小改动，直击根因）

- 16 个 worker 抓取（HTTP）仍并行，但 DB 写阶段通过互斥锁/信号量串行化（同一时刻仅 1 个写者进入 `RefreshFeedEntries` 写段）。
- 由于单连接本来就必须串行，限流**不损失写吞吐**，却能显著减少连接排队争抢。
- 优点：改动几行、风险最低、实测已证根因即为连接排队。
- 风险：需确认抓取与写阶段的分离点，避免误锁抓取部分。

### 方案 D：读路径短缓存（缓解感知）

- 对 `GetNavMetadata` 的未读 count 做短 TTL 缓存（如 5–10 s）。
- 优点：改动小，改善首屏感知。
- 缺点：仅缓解，不解决根本。

## 7. 建议落地路径

1. **首选方案 C**：最小、安全、直击实测根因（连接排队）。
2. 若需彻底根治，再评估**方案 A**（读写双连接）。
3. 无论选哪个，改动前都应先给出改动清单供审核，并遵循项目"未经确认禁止修改"的规则。

---

## 附录：验证方法说明

- 临时验证程序复用项目自身连接池（`MaxOpenConns=1`）与存储层，行为贴近生产。
- 写操作均在事务内执行后回滚，未对 `miniflux.db` 产生任何数据污染（验证后 `git status` 干净）。
- 临时诊断程序已删除，工作区无残留。
