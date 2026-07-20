# Miniflux PostgreSQL → SQLite 迁移报告

> **状态**：编译通过 / vet 通过 / 全量测试通过 / 生产运行验证通过  
> **迁移方式**：纯 Go 驱动 `modernc.org/sqlite`（无 CGO）  
> **最后更新**：2026-07-18

---

## 一、文件变更清单

### 新增

| 文件 | 说明 |
|------|------|
| `internal/database/sqlite.go` | SQLite 连接池 + DSN（单连接 / busy_timeout=30000 / WAL / foreign_keys） |
| `internal/model/sqlscan.go` | `BoolScanner`、`StringArrayScanner`、`Time` 扫描器、`TagsValue`、`JSONValue` |
| `internal/storage/helpers.go` | `inClauseInt64` / `inClauseString` 替代 `pq.Array` / `= ANY()` |
| `internal/storage/storage_test.go` | SQLite 集成测试（迁移→建表→CRUD→FTS5→tags→bool→时间扫描） |
| `packaging/docker/alpine/entrypoint.sh` | 入口脚本：root 修目录权限后 su-exec 切 nobody |

### 删除

| 文件 | 说明 |
|------|------|
| `internal/database/postgresql.go` | PostgreSQL 连接池 |
| `internal/storage/entry_test.go` | 测试已删除的 `truncateStringForTSVectorField` |
| `internal/storage/_pg2sqlite_phconv.py` | 一次性占位符转换脚本 |

### 修改

| 文件 | 核心改动 |
|------|----------|
| `go.mod` / `go.sum` | 移除 `lib/pq`，新增 `modernc.org/sqlite` |
| `internal/database/migrations.go` | 全部旧迁移→单一 SQLite schema + FTS5 + 触发器 |
| `internal/database/database.go` | `$1`→`?`、`TRUNCATE`→`DELETE` |
| `internal/config/options.go` | `DATABASE_URL`→`miniflux.db`；`DATABASE_MAX_CONNS`→`1` |
| `internal/config/options_parsing_test.go` | 默认值 + `MAX_CONNS` 断言更新 |
| `internal/storage/entry.go` | tsvector/CTE/starred/time scan/`changed_at`/Archiving/MarkFeedAsRead/VACUUM |
| `internal/storage/entry_query_builder.go` | FTS5 MATCH、tags→json_each、bool/time scan、ORDER BY 表前缀 |
| `internal/storage/entry_pagination_builder.go` | FTS5/tags/`$N`→`?N`/`quoteIdent` |
| `internal/storage/feed.go` | CheckedAt time scan、bool scan、`SELECT true`→`SELECT 1` |
| `internal/storage/feed_query_builder.go` | `$N` residual、`at time zone`、bool/time scan、ORDER BY 表前缀 |
| `internal/storage/category.go` | `pq.Array`→`inClauseString`、`$N`→`?N` |
| `internal/storage/user.go` | `now()`→`strftime`、`SELECT true`→`SELECT 1`、time scan |
| `internal/storage/api_key.go` | `now()`→`strftime`、`SELECT true`→`SELECT 1`、time scan |
| `internal/storage/enclosure.go` | `pq.Array`→`inClauseInt64/String`、`ON CONFLICT` 键调整 |
| `internal/storage/icon.go` | `SELECT true`→`SELECT 1` |
| `internal/storage/webauthn.go` | `NullBool`→`bool`、`AddedOn`+`LastSeenOn` time scan |
| `internal/storage/web_session.go` | `::interval`→应用层计算、`CreatedAt` time scan |
| `internal/storage/batch.go` | `$N`→`?N`、`IS false`→`= 0` |
| `internal/storage/certificate_cache.go` | `::bytea`/`now()`→`strftime` |
| `internal/storage/integration.go` | `='t'`→`= 1`、`SELECT true`→`SELECT 1` |
| `internal/storage/nav_metadata.go` | `='t'`→`= 1`、`IS FALSE`→`= 0` |
| `internal/storage/storage.go` | `server_version`→`sqlite_version()`、`pg_size_pretty`→`pragma`、`Vacuum()` |
| `internal/cli/cleanup_tasks.go` | 清理末尾 VACUUM |
| `internal/cli/cleanup_tasks.go` | 清理末尾 VACUUM |
| `internal/ui/about.go` | `postgres_version`→`sqlite_version` |
| `internal/template/templates/views/about.html` | 同上 |
| `internal/locale/translations/*.json`（24 文件） | 新增 `page.about.sqlite_version` |
| `packaging/docker/alpine/Dockerfile` | 入口脚本 / DATABASE_URL / VOLUME |
| `packaging/docker/distroless/Dockerfile` | DATABASE_URL / VOLUME |
| `README.md` | PostgreSQL→SQLite |
| `Makefile` | `DB_URL` 改为文件，集成测试清理 |
| `CONTRIBUTING.md` | 数据库需求改为 SQLite |

---

## 二、功能差异

### 2.1 全文搜索

| | PostgreSQL | SQLite |
|---|---|---|
| 索引引擎 | `tsvector` + GIN + `setweight` | FTS5 external-content 虚拟表 + 3 触发器 |
| 查询语法 | `document_vectors @@ websearch_to_tsquery('…')` | `entries_fts MATCH ?` |
| 结果排序 | 按 `ts_rank` 相关性 | **按时序排列**（无相关性排序） |
| 分词器 | PG 内置多语言 | `unicode61` |

### 2.2 数据库运维

| | PostgreSQL | SQLite |
|---|---|---|
| 服务器 | 独立 PG 实例 | **嵌入式，零外部依赖** |
| 连接方式 | TCP `host:port/dbname` | 本地文件（`miniflux.db` / `:memory:`） |
| 并发 | 原生多连接 | **单连接（MaxOpenConns=1）**，WAL 读不阻塞 |
| 大小查询 | `pg_size_pretty(pg_database_size())` | `page_count * page_size` + `formatBytes` |
| 磁盘回收 | 自动 | 定时清理末尾 + FlushHistory 后自动 `VACUUM` |

### 2.3 SQL 语法差异

| PG | SQLite | 备注 |
|---|---|---|
| `$1, $2, ...` | `?1, ?2, ...` | |
| `RETURNING` | **保留** | ≥3.35 |
| `ON CONFLICT DO UPDATE` | **保留** | ≥3.35 |
| `ILIKE` | `LOWER(x) LIKE LOWER(y)` | 索引不生效 |
| `DISTINCT ON` | 窗口函数 `row_number() OVER (PARTITION BY ...)` | |
| data-modifying CTE | **拆为多条 SQL** | SQLite 不支持 |
| `::bytea` / `::text` / `::interval` | **移除** | |
| `at time zone` | **移除** | 应用层 `timezone.Convert` |
| `now()` SQL 函数 | `strftime('%Y-%m-%dT%H:%M:%SZ', 'now')` | |
| `= ANY($N)` / `<> ALL($N)` | `IN (…)` / `NOT IN (…)` | `inClauseInt64/String` |
| `pq.Array()` / `pq.QuoteIdentifier()` | `TagsValue` / `quoteIdent` | |
| `SELECT true` | `SELECT 1`→`int` | |
| `FOR UPDATE SKIP LOCKED` | **删除** | |
| `ORDER BY "id"` | `e."id"` / `f."id"` 加表前缀 | 多表 JOIN 歧义 |
| `TRUNCATE` | `DELETE FROM` | |
| `'t'/'f'` | `= 1` / `= 0` | INTEGER 列 |
| `published_at < ?`（TEXT 字符串比较） | **移除** | 跨时区不可靠 |

### 2.4 数据类型映射

| PG 类型 | SQLite 类型 | 读取方式 |
|---|---|---|
| `jsonb` | TEXT | 纯字符串 |
| `text[]` | TEXT（JSON 数组） | `StringArrayScanner` |
| `timestamptz` | TEXT | `model.Time` Scanner（8 种格式 + 双时区/单调时钟） |
| `bool` | INTEGER（0/1） | `model.BoolScanner` |
| `inet` | TEXT | |
| `bytea` | BLOB | |
| `bigserial` | `INTEGER PRIMARY KEY AUTOINCREMENT` | |
| `enum` | TEXT + 应用层校验 | |

### 2.5 Go 依赖

| | 移除 | 新增 |
|---|---|---|
| 直接 | `github.com/lib/pq` | `modernc.org/sqlite v1.34.4` |
| 间接 | — | `modernc.org/libc`、`mathutil`、`memory` |

---

## 三、运行时 Bug 修复清单（六轮排查）

| # | 文件 | 问题 | 影响 |
|---|------|------|------|
| 1 | `migrations.go` | 6 张表重复 `primary key(id)` | 建表失败 |
| 2 | `migrations.go` | `feeds` 缺 `disable_http2` 列 | CreateFeed 失败 |
| 3 | 全部 storage | TEXT 时间扫不进 `time.Time` | 读时间崩溃 |
| 4 | `entry.go` | `createEntry` 漏 `starred` 列 | 星标丢失 |
| 5 | `entry.go` | `updateEntry` 占位符编号错位 | 写错字段 |
| 6 | `entry.go` | `updateEntry` 缺 `changed_at` | changed_at 不更新 |
| 7 | `entry_query_builder.go` | `WithEntryIDs`/`WithStatuses` 占位符翻倍跳 | `?N` 超界 |
| 8 | `feed_query_builder.go` | `counterConditions` 遗漏 `$N` | 计数器 SQL 失败 |
| 9 | `integration.go` | `SELECT true`→`bool` 扫描×2 | 用户名检查失败 |
| 10 | `feed.go` | `CheckedAt` `time.Time` 直接扫描 | 查询失败 |
| 11 | `webauthn.go` | `AddedOn` `*time.Time` 直接扫描×2 | WebAuthn 崩溃 |
| 12 | `model/sqlscan.go` | 不兼容单调时钟 ` m=+...` | 时间扫不回 |
| 13 | `model/sqlscan.go` | 不兼容双时区 `+0800 +0800` | 中国时区失败 |
| 14 | `entry.go` | `SetEntriesStatusAndCountVisible` CTE | 标记状态 500 |
| 15 | `entry.go` | `ArchiveEntries` CTE | 归档静默失败 |
| 16 | `entry.go` | `FlushHistory` CTE | 清空历史失败 |
| 17 | `database/sqlite.go` | 多连接 + busy_timeout 不够 | SQLITE_BUSY |
| 18 | `entry.go` | `MarkFeedAsRead` `published_at < ?` TEXT 跨时区 | 标记已读不生效 |
| 19 | `storage.go` | 删除后文件不缩 | 磁盘浪费 |

### 连接池优化

原方案**多连接 + busy_timeout=5000** 在批量刷新时持续 `SQLITE_BUSY`。  
改为 **单连接（MaxOpenConns=1）+ busy_timeout=30000**，内部自然串行化。

### 时间扫描器

`model.Time.Scan` 支持 8 种格式，额外处理：
- ` m=+163.78...` 单调时钟后缀（`strings.Index` 剥离）
- ` +0800 +0800` 双数字时区（正则剥离）

### MarkFeedAsRead 时区问题（#18）

SQLite TEXT 列做纯字符串比较，不感知时区。`published_at` 存 `+0800` 格式，`time.Now()` 可能返回 `+0000` UTC，`17:17 +0800` > `10:XX +0000`（字符串序）。已移除 `published_at < ?` 过滤。

### VACUUM 磁盘回收（#19）

SQLite 删除数据后不自动缩文件。两处自动触发：
- 定时清理任务末尾（每 24 小时）
- `FlushHistory` 完成后

---

## 四、Docker 部署

### Alpine（推荐）

```dockerfile
FROM golang:alpine3.23 AS build
# ... make miniflux ...

FROM alpine:3.24
ENV DATABASE_URL=/var/lib/miniflux/miniflux.db
RUN apk add --no-cache ca-certificates tzdata su-exec && mkdir -p /var/lib/miniflux
COPY packaging/docker/alpine/entrypoint.sh /entrypoint.sh
VOLUME /var/lib/miniflux
ENTRYPOINT ["/entrypoint.sh"]
CMD ["/usr/bin/miniflux"]
```

`entrypoint.sh`：启动时 root `chown`，再 `su-exec` 切 nobody。

```sh
#!/bin/sh
chown -R 65534 /var/lib/miniflux 2>/dev/null
exec su-exec 65534 "$@"
```

> **注意**：命名卷（`-v name:/var/lib/miniflux`）安全不影响宿主机。bind mount 会改宿主机目录 UID，需手动 `chown 65534 目录` 或改用 `/tmp/`。

### 编译 & 交叉编译

```bash
# Windows
go build -o miniflux.app.exe .

# Linux（交叉编译，无 CGO）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o miniflux .
```

### 运行

```bash
docker run -p 8081:8080 --name miniflux2 \
  -v miniflux_data:/var/lib/miniflux \
  -e "RUN_MIGRATIONS=1" -e "CREATE_ADMIN=1" \
  -e "ADMIN_USERNAME=admin" -e "ADMIN_PASSWORD=admin123" \
  miniflux-sqlite:latest
```

### 热更新

```bash
go build -o miniflux .
docker cp ./miniflux miniflux2:/usr/bin/miniflux
docker restart miniflux2
```

---

## 五、时区设置

```bash
curl -u admin:admin123 -X PUT http://localhost:8080/v1/users/1 \
  -H "Content-Type: application/json" \
  -d '{"timezone": "Asia/Shanghai"}'
```

`at time zone` 已从 SQL 移除，由应用层 `timezone.Convert` 处理。

---

## 六、过期清理

| 配置项 | 默认值 | 效果 |
|--------|--------|------|
| `CLEANUP_FREQUENCY_HOURS` | 24 | 执行间隔 |
| `CLEANUP_ARCHIVE_READ_DAYS` | 60 | 已读条目保留天数 |
| `CLEANUP_ARCHIVE_UNREAD_DAYS` | 180 | 未读条目保留天数 |
| `CLEANUP_ARCHIVE_BATCH_SIZE` | 10000 | 每次最大删除量 |

自动流程：Sessions cleanup → Archive Read → Archive Unread → Orphan Icons → **VACUUM**

手动触发：`docker exec miniflux2 /usr/bin/miniflux -run-cleanup-tasks`

---

## 七、已知限制

1. **全文搜索无相关性排序**
2. **`ILIKE` 降级**——`LOWER LIKE` 无法使用索引
3. **无 `FOR UPDATE SKIP LOCKED`**
4. **无增量迁移**——单一初始 schema
5. **单连接架构**——写入串行化，WAL 下读取不受限
6. **不包含 PG→SQLite 数据迁移工具**
7. **bind mount 需手动调目录权限**——命名卷无此问题

---

## 八、验证状态

| 检查项 | 结果 |
|--------|------|
| `go build .` | ✅ |
| `go vet ./...` | ✅ |
| `go test ./...`（~56 包） | ✅ |
| Docker 部署 + 抓取 + 阅读 | ✅ |
| Web UI 标记已读 / 清空历史 | ✅ |
| FTS5 搜索 | ✅ |
| 时间戳读写（含中国时区/单调时钟） | ✅ |
| Bool 列（INTEGER↔bool） | ✅ |
| Tags JSON（`json_each`） | ✅ |
| `RETURNING` / `ON CONFLICT DO UPDATE` | ✅ |
| 过期清理 + VACUUM | ✅ |
| WebAuthn | ✅ |
