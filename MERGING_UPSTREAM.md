# 上游合并说明文档（Upstream Merge Log）

> **每次发生代码合并（`git merge origin/main`）之前，必须先阅读本文件**，尤其关注下方「合并前必须检查」与「历次合并注意事项」两个章节，再开始操作。
>
> **详细参考**：本 fork 的 SQLite 改造全貌（迁移细节、类型映射、运行时 Bug 修复、Docker 部署、验证状态）见同目录 `MIGRATION_SQLITE.md`。合并存储层代码时，请配合阅读该文件相关章节。

---

## 一、本仓库背景（务必先读）

本仓库 `miniflux.app/v2` 是 **miniflux 的 SQLite fork**（`miniflux-v2-sqlite`），而上游 `origin`（`https://github.com/miniflux/v2.git`）是 **PostgreSQL 版本**。两者在存储层存在持久性差异，合并上游代码时必须持续保持这些差异，否则会导致 SQLite 运行时报错或编译失败。

### 已识别的差异点

| 差异项 | 本 fork（SQLite） | 上游（PostgreSQL） |
| --- | --- | --- |
| 查询占位符 | `?1`, `?2` … | `$1`, `$2` … |
| 存在性查询取值 | `SELECT 1 FROM …` | `SELECT true FROM …` |
| SQLite 专用驱动 | `modernc.org/sqlite` | `github.com/lib/pq` |
| SQLite 间接依赖 | `modernc.org/*`, `github.com/remyoudompheng/bigfft`, `github.com/hashicorp/golang-lru/v2` 等 | — |

> 注意：SQLite 方言统一使用 `?N` 形式占位符；上游若引入 PostgreSQL 专用的 `$N` 语法，必须在合并时改回 `?N`。

### SQL 语法差异对照表（合并存储层冲突时的核心依据）

> 完整说明见 `MIGRATION_SQLITE.md` §2.3。此表为合并时的速查对照。

| PG 语法 | SQLite 写法 | 备注 |
| --- | --- | --- |
| `$1, $2, ...` | `?1, ?2, ...` | 占位符统一替换 |
| `SELECT true` | `SELECT 1` → `int` | 存在性查询 |
| `'t'` / `'f'` | `= 1` / `= 0` | INTEGER 列 |
| `ILIKE` | `LOWER(x) LIKE LOWER(y)` | 索引不生效 |
| `= ANY($N)` / `<> ALL($N)` | `IN (…)` / `NOT IN (…)` | 用 `inClauseInt64/String` |
| `pq.Array()` / `pq.QuoteIdentifier()` | `TagsValue` / `quoteIdent` | |
| `DISTINCT ON` | 窗口函数 `row_number() OVER (PARTITION BY …)` | |
| `::bytea` / `::text` / `::interval` | **移除** | |
| `at time zone` | **移除**，应用层 `timezone.Convert` | |
| `now()` | `strftime('%Y-%m-%dT%H:%M:%SZ', 'now')` | |
| data-modifying CTE | **拆为多条 SQL + 事务包裹** | SQLite 不支持；单连接下事务内 de-facto 原子 |
| `FOR UPDATE SKIP LOCKED` | **删除** | |
| `ORDER BY "id"` | `e."id"` / `f."id"` 加表前缀 | 多表 JOIN 歧义 |
| `TRUNCATE` | `DELETE FROM` | |
| 时间列比较 `column < ?` | 直接字符串比较（RFC3339 字典序） | 传参 `time.Time.UTC().Format(time.RFC3339)`，可利用 B-tree 索引 |

### Go 依赖对照表

| | 移除（不可引入） | 新增（必须保留） |
| --- | --- | --- |
| 直接 | `github.com/lib/pq` | `modernc.org/sqlite v1.34.x` |
| 间接 | — | `modernc.org/libc`、`modernc.org/mathutil`、`modernc.org/memory`、`modernc.org/strutil`、`modernc.org/token`、`modernc.org/gc/v3`、`github.com/remyoudompheng/bigfft`、`github.com/hashicorp/golang-lru/v2` 等 |

### 迁移方式（背景）

纯 Go 驱动 `modernc.org/sqlite`（无 CGO），嵌入本地文件 `miniflux.db`；单连接（`MaxOpenConns=1`）+ `busy_timeout=30000` + WAL + `foreign_keys`。详见 `MIGRATION_SQLITE.md` §一、§2.2。

---

## 二、合并前必须检查（Checklist）

每次 `git merge origin/main` 之前，按以下顺序检查并记录：

0. **合并前记录本地版本信息**：记录 `git rev-parse HEAD`（合并前本地 `main` 的 HEAD commit）+ 首行提交信息 + 本地领先上游的提交数。
1. **`git fetch origin`** 获取最新上游。
2. **`git merge origin/main`** 开始合并。
3. **记录合并的上游版本信息**：记录上游起点与终点 commit（`<旧HEAD>..<新HEAD>`）、`git describe --tags <新HEAD>` 对应的版本标签、拉取到的新标签。
4. 对每个冲突文件，判断冲突属于「上游重构（应跟随）」还是「方言差异（应保留 SQLite 版）」：
   - 函数签名/逻辑重构 → 跟随上游（保持与调用方一致）。
   - 存储层 SQL 语法/驱动 → 保留本 fork 的 SQLite 方言。
5. 处理 `go.mod` / `go.sum`：
   - **不要引入** `github.com/lib/pq`（PostgreSQL 驱动）。
   - 保留 `modernc.org/sqlite` 及其间接依赖。
   - 其余依赖版本升级可正常跟随。
   - 合并冲突后运行 **`go mod tidy`** 整理依赖。
6. 验证：**`go build ./...`** 必须通过，且无 lint 错误。
7. 全部通过后 `git add` + `git commit` 完成合并提交，并**在「历次合并记录」中追加完整记录**（含合并的上游版本信息、合并前的本地版本信息）。

### 合并上游新代码时需警惕的 SQLite 特有坑

> 这些坑源自 `MIGRATION_SQLITE.md` §三（运行时 Bug 修复清单），合并涉及 storage 层的新增/改动函数时重点核查：

1. **时间扫描**：TEXT 时间列不能直接 `Scan(&time.Time{})`，必须用 `model.Time` 扫描器；`*time.Time` 同样需要处理（如 `AddedOn`、`LastSeenOn`、`CheckedAt`）。
2. **bool 扫描**：INTEGER（0/1）列不能用 `bool` 直接扫 PG 风格，需确认扫描方式（`model.BoolScanner` 或 `SELECT 1`→`int`）。
3. **占位符编号**：`WithEntryIDs`/`WithStatuses`/`counterConditions` 等动态拼接 SQL 中 `?N` 编号易错位/翻倍，新增此类逻辑务必核对编号。
4. **CTE / data-modifying CTE**：SQLite 不支持，必须拆为多条语句 + 事务包裹。
5. **时间比较**：统一用直接字符串比较（RFC3339 字典序），不要引入 `CAST(strftime('%s', …) AS INTEGER)`（无法用索引）。
6. **新增列**：上游若为表新增列，必须同步在 `internal/database/migrations.go` 的 SQLite schema 中补列，否则 CRUD 失败（历史教训：`feeds.disable_http2` 缺失导致 CreateFeed 失败）。
7. **`now()` / `at time zone` / `::interval`**：上游新 SQL 若含此类 PG 语法，需改 `strftime` / 应用层转换。

---

## 三、SQLite 已知限制（判断上游新功能是否兼容）

> 完整说明见 `MIGRATION_SQLITE.md` §七。合并上游新功能时，先判断其是否触达以下限制。

1. **全文搜索**：分词器为 `unicode61`，对 CJK 效果有限（已支持 `bm25()` 相关性排序）。
2. **`ILIKE` 降级**：`LOWER LIKE` 无法使用索引。
3. **无 `FOR UPDATE SKIP LOCKED`**。
4. **无增量迁移**：单一初始 schema（`migrations` 数组保留版本号机制，可追加未来迁移）。
5. **单连接架构**：写入串行化，WAL 下读取不受限。
6. **不包含 PG→SQLite 数据迁移工具**。
7. **bind mount 需手动调目录权限**（Docker），命名卷无此问题。

---

## 四、通用注意事项（总览）

1. **方言优先**：所有存储层 SQL 一律保持 SQLite 方言（`?N` 占位符）。
2. **跟随重构、保留方言**：函数签名与逻辑重构跟随上游；仅 SQL 语法与驱动选择保留 fork 版。
3. **`lib/pq` 禁止引入**。
4. **用 `go mod tidy` 兜底**，再 `go build ./...` 验证。
5. **存储层新增/改动**：合并涉及 storage 层时，对照「SQL 语法差异对照表」与「SQLite 特有坑」逐项核查。
6. **每次合并后**：更新本文件，追加一条「历次合并记录」，并复核「合并前必须检查」清单是否有新增注意事项。
7. **版本信息必填**：每条合并记录必须同时包含「合并的上游版本信息」（commit 起止 + 版本标签）与「合并前的本地版本信息」（本地 HEAD commit + 描述），缺一不可。

---

## 五、历次合并记录（Merge Log）

> **本章节放在文档末尾，便于每次合并后在文件结尾追加新记录。**
>
> **每条记录必须包含两类版本信息**：
> - **合并的上游版本信息**：上游 commit 起止范围、上游对应版本号/标签。
> - **合并前的本地版本信息**：本地 `main` 合并前的 HEAD commit 哈希及其版本描述（本地 fork 通常无版本标签，以 commit 哈希 + 首行提交信息为准）。

### 2026-08-10 — 合并上游至 `2f9e07bd`

- **合并日期**：2026-08-10
- **合并提交**：`cf26371a`（Merge remote-tracking branch 'origin/main'）

#### 合并的上游版本信息

- **上游起点**：`c4d54f87`（build(deps): bump github.com/prometheus/client_golang in the gomod group）
- **上游终点**：`2f9e07bd`（fix(googlereader): raise item IDs limit to 10000）
- **上游范围**：`c4d54f87..2f9e07bd`
- **上游版本/标签**：本次合并拉取到标签 `2.3.3` / `v2.3.3`；上游 main 终点 `2f9e07bd` 为 `v2.3.3` 之后第 10 个提交（`v2.3.3-10-g2f9e07bd`），**尚未发布对应版本标签**
- **合并提交数**：10 个

#### 合并前的本地版本信息

- **合并前本地 HEAD**：`5bccb532`（perf(storage): eliminate window-function full scans in history queries）
- **本地版本描述**：本地 fork 无版本标签，仅以 commit 哈希标识；本地曾领先 `origin/main` 14 个提交（14 个本地功能提交）
- **本地分支**：`main`

#### 上游提交列表

- `2f9e07bd` fix(googlereader): raise item IDs limit to 10000
- `b3039d6c` fix(ui): add accessible name to documentation links
- `3b5a7ee4` fix(sanitizer): reject non-web schemes in iframe source URLs
- `69756868` refactor: change existence-check helpers to return (bool, error)
- `2bc9f306` fix(ui): guard against nil session in CSRF middleware
- `681b00f6` perf(internal): reorder some struct members
- `a694ee71` fix: enforce domain boundary in YouTube URL hostname check
- `6ed1006a` fix: log swallowed DB errors in existence-check helpers
- `4998282d` build(deps): bump the github-actions group with 10 updates
- `ac24f736` build(deps): bump the gomod group with 2 updates

#### 冲突文件（4 个）

| 文件 | 冲突类型 | 处理结论 |
| --- | --- | --- |
| `internal/storage/category.go` | 方言 + 重构 | 跟随上游新签名 `(bool, error)`，保留 SQLite 语法 `SELECT 1 … ?1` |
| `internal/storage/feed.go` | 方言 + 重构 | 同上，`FeedExists` 与 `CategoryFeedExists` 两处均如此处理 |
| `go.mod` | 依赖 | 升级依赖版本；**移除** `github.com/lib/pq`；保留 SQLite 间接依赖 |
| `go.sum` | 依赖 | 采用升级版本（如 `klauspost/compress v1.19.1`），保留 SQLite 依赖条目；随后用 `go mod tidy` 整理 |

#### 关键结论

- 上游提交 `69756868` 与 `6ed1006a` 将存在性检查函数（`CategoryIDExists`、`FeedExists`、`CategoryFeedExists`）签名从 `bool` 改为 `(bool, error)`，并更新了所有调用方。本 fork 必须**跟随此签名变化**（保证与已自动合并的调用方一致），但 SQL 查询体保留 SQLite 方言。
- `go.mod` 中**不要**加入 `github.com/lib/pq`，即使上游引入。
- 合并后执行 `go mod tidy` 可自动补齐/清理 SQLite 间接依赖，无需手工维护 go.sum。

#### 验证结果

- `go build ./...` 通过。
- `internal/storage/category.go`、`internal/storage/feed.go` 无 lint 错误。

#### 注意事项（本次新发现，后续合并沿用）

1. 当上游重构存储层函数签名时，调用方文件通常已被 git 自动合并（无冲突），只有被改函数所在的文件报冲突。此时应**以调用方的期望签名为准**反向决定被改函数的最终形态。
2. 合并 go.mod 冲突时，逐行核对是否含 `lib/pq`、是否保留了 `modernc.org/*` 等 SQLite 依赖。
3. 每次合并后统一用 `go mod tidy` + `go build ./...` 兜底验证，避免手工遗漏 go.sum 条目。

### 2026-11-18 — 合并上游至 `b31f7867`

- **合并时间**：2026-11-18
- **合并提交**：本记录所在的合并提交（`Merge remote-tracking branch 'origin/main'`）

#### 合并的上游版本信息

- **上游起点**：`2f9e07bd`（fix(googlereader): raise item IDs limit to 10000）
- **上游终点**：`b31f7867f4a1ad9b4abfcebd376c94cecf5f1942`（fix(urllib): handle RFC 8215 local-use NAT64 prefix in SSRF filter）
- **上游范围**：`2f9e07bd..b31f7867`
- **上游版本/标签**：`v2.3.3-88-gb31f7867`（未发布新标签）
- **上游提交数**：78 个（`171 files changed, 6452 insertions(+), 1316 deletions(-)`）

#### 合并前的本地版本信息

- **合并前本地 HEAD**：`ecefc001663986c1f56e6383e5286da911ccac90`（feat: add DISABLE_FULL_TEXT_SEARCH to disable FTS5 at runtime）
- **本地版本描述**：`v2.3.3-30-gecefc001`，领先 `origin/main` 20 个提交
- **本地分支**：`main`

#### 上游提交列表

- `b31f7867 fix(urllib): handle RFC 8215 local-use NAT64 prefix in SSRF filter`
- `fb043227 docs: rewrite security policy`
- `1af9501a build(deps): bump the gomod group with 2 updates`
- `3ad08c4b build: add freebsd-arm64 target`
- `9447aaec fix(fever): compare API key digests to avoid timing side-channel`
- `64bdc5fe fix(urllib): treat 0.0.0.0/8 and 240.0.0.0/4 as non-public`
- `052409ef fix(api): delete users synchronously and report failures`
- `ba40f99f chore(contrib): pin postgres to 18 in Docker Compose examples`
- `1947089d feat(locale): add Vietnamese translation`
- `faecfa2f fix(storage): look up categories by title case-insensitively`
- `df363735 fix(googlereader): avoid infinite recursion in StreamType.String`
- `d57da7c7 fix(fever): honor the before timestamp when marking all items as read`
- `a0cc2e44 fix(fever): set the starred state instead of toggling it`
- `707d3c39 fix(locale): add or reuse translation keys missing from all locales`
- `400e6b6a fix(validator): validate username format on modification`
- `b8da6886 fix(ui): stop caching shared entry pages in public caches`
- `378ce7df fix(ui): only persist web sessions on routes that start a flow`
- `bb61fe0b build(deps): update Go dependencies`
- `70adb668 docs(man): review man page for accuracy and readability`
- `b475d6bb fix(oauth2): read the OIDC group claim from the ID token too`
- `14c070be build(deps): bump the github-actions group with 6 updates`
- `5f881adb ci: harden GitHub Actions workflows`
- `752793f2 fix(ui): prevent administrators from editing their own account`
- `f654f7c7 feat(ui): ask for the current password before changing it`
- `229035a4 fix(security): revoke other sessions when the password changes`
- `572ac7d7 refactor(ui): move authentication settings to a dedicated page`
- `0877d29e fix(security): only send feed cookies to the feed's website`
- `7833b224 fix(googlereader): avoid logging the auth token in clear text`
- `0388db74 build(deps): bump github.com/go-jose/go-jose/v4 from 4.1.4 to 4.1.6`
- `8542ea48 build(deps): bump the gomod group with 2 updates`
- `3945f3da fix(security): block 6to4 and IPv4-compatible addresses in SSRF filter`
- `d5c529b9 fix(ui): don't create web sessions for unauthenticated protected routes`
- `12eb4d80 test(date): check the trimmed lower-case date in its own test`
- `04fc79f7 fix(date): parse RFC 3339 dates with a lower-case t or z`
- `5f5efd75 fix(auth): enable WebAuthn-only login and document setup`
- `000f8ce2 feat(auth): allow WebAuthn with disabled local authentication`
- `a4c00331 fix(fever): always order the items endpoint by entry id`
- `54e2cc7d refactor(model): remove unused WebSession language and theme setters`
- `030ecebf fix(ui): keep logged-out preferences in a cookie instead of the session`
- `8de58f8b build(deps): bump the gomod group with 3 updates`
- `bdd8087d fix(ui): don't create web sessions for media proxy requests`
- `927e0e86 fix(json): accept JSON feeds that start with a UTF-8 byte order mark`
- `97fa1d37 fix(http): take the client IP from the right side of X-Forwarded-For`
- `fb4f1b03 feat(rewrite): reject invalid CSS selectors in rewrite and scraper rules`
- `c573f2e0 feat(api): add force parameter to the feed refresh endpoint`
- `05b24c91 feat(oauth2): admin role mapping`
- `71091f06 feat(locale): update Polish translation`
- `238c5614 fix(security): block reserved IP ranges in SSRF filter`
- `214aca7d fix(gotify): avoid nil feed dereference in notifications`
- `942ea1d9 fix(security): prevent WebAuthn open redirect`
- `f17c8fd3 fix(fetcher): restrict private per-feed proxy access`
- `f66e8760 feat(integration): add Gotify push notification support`
- `71cfdb8b feat(scraper): Added 2 Steam related scraper rules`
- `5dadd25b docs: document the fixed Unix socket permissions in LISTEN_ADDR`
- `0cde2699 build(deps): bump the gomod group with 6 updates`
- `c317f84b fix(server): add timeouts to ACME challenge listener`
- `5c80660a fix(response): prefer server order to client order for response encoding`
- `6333ca10 feat(rewrite): validate rewrite rules at save time with helpful error messages`
- `249b340c fix(karakeep): report the HTTP status when saving an entry fails`
- `f4f655b3 refactor(response): unified response builder`
- `899fe04c build(deps): bump the gomod group with 2 updates`
- `c52bdef6 fix(ui): restart pagination when the feed or category page falls past the end`
- `b683d3b4 fix(fetcher): trust every proxy selected during a redirect chain`
- `cedbb394 fix(fetcher): allow connecting to a proxy set through the environment`
- `434263cb build(deps): bump the github-actions group with 6 updates`
- `703fe826 build(deps): bump the gomod group across 1 directory with 2 updates`
- `4e6d7f93 build(deps): bump the gomod group with 7 updates`
- `ca552a00 feat(locale): update Polish translation`
- `76889f08 fix(auth): enforce DISABLE_LOCAL_AUTH on the API and password changes`
- `589659ff fix(googlereader): reject auth tokens when no password is set`
- `bbf082ab build(deps): bump the gomod group across 1 directory with 6 updates`
- `562c0271 build(deps): bump the github-actions group with 4 updates`
- `a84533db perf(database): index enclosure URL digest as bytea instead of hex`
- `b79ba933 fix(urllib): reject surrounding whitespace in IsRelativePath`
- `a7f93d8b fix: avoid panic when creating a feed without any category`
- `ca241333 perf(database): drop redundant entries_user_status_changed_idx`
- `59c15737 build(deps): bump the gomod group across 1 directory with 5 updates`
- `106cdd09 fix(webauthn): preserve redirect_url after passkey login`

#### 冲突文件（7 个 UU + 1 个 DU）

| 文件 | 冲突类型 | 处理结论 |
| --- | --- | --- |
| `go.mod` | 依赖 | 采用上游全部版本升级；**删除** `github.com/lib/pq`；保留 `modernc.org/sqlite` 及全部 SQLite 间接依赖；新增上游的 `github.com/pandatix/go-abnf v0.5.5`；随后 `go mod tidy` |
| `go.sum` | 依赖 | 以 `origin/main` 版本为基线，再用 `go mod tidy` 重新生成（137 行） |
| `internal/storage/category.go` | 方言 + 功能 | 跟随上游「按标题大小写不敏感查找」，SQL 改写成 SQLite 方言：`SELECT id, user_id, title, hide_globally FROM categories WHERE user_id=?1 AND lower(title)=lower(?2) ORDER BY title=?2 DESC, id ASC LIMIT 1`，走 `s.reader()` |
| `internal/storage/enclosure.go` | 方言 | 保留 fork 的 `ON CONFLICT (user_id, entry_id, url) DO NOTHING` 与 `?1..?6`；**不采用**上游 `sha256(url::bytea)` 摘要唯一索引 |
| `internal/storage/integration.go` | 方言 + 安全 | `UpdateIntegration` 保留 fork 的 `?1..?126` 全量编号，并补上上游 gotify 四列（`gotify_enabled=?122` … `gotify_priority=?125`、`user_id=?126`）；`UserByFeverToken` 因 SQLite 无 `sha256()` 改为 Go 侧 `crypto/sha256` + `subtle.ConstantTimeCompare`；两处 googlereader 条件补 `AND integrations.googlereader_password <> ''` |
| `internal/storage/web_session.go` | 方言 + 新功能 | 采用上游新增的 `RemoveUserWebSessionsExcept(userID int64, keepSessionID string)` 与 `anonymousWebSessionLifetime = 24 * time.Hour`；`CleanOldWebSessions` 把 `now() - $1::interval` 改写为两条 RFC3339 字符串比较：`DELETE FROM web_sessions WHERE created_at < ?1 OR (user_id IS NULL AND created_at < ?2)` |
| `internal/template/functions.go` | 功能 | 合并两边键集：保留 fork 的 `fullTextSearchEnabled`，新增上游的 `webAuthnEnabled` |
| `miniflux.1` | modify/delete（DU） | **保留**该文件（fork 曾在 `059c03b5` 删除）。恢复上游 761 行版本，再把 DESCRIPTION、`DATABASE_URL`、EXAMPLES 改为 SQLite 语义。原因：`packaging/debian/build.sh`、`packaging/debian/miniflux.manpages`、`packaging/rpm/Dockerfile`、`packaging/rpm/miniflux.spec`、`packaging/systemd/miniflux.service` 均引用它 |

未冲突但必须做方言改写的文件：

| 文件 | 上游改动 | 方言改写 |
| --- | --- | --- |
| `internal/database/migrations.go` | Gotify 新增四列（`ALTER TABLE integrations ADD COLUMN … , ADD COLUMN …`）；enclosures 唯一索引改为 `sha256(url::bytea)`；删除冗余索引 `entries_user_status_changed_idx` | SQLite 不支持一条 `ALTER TABLE` 多列 `ADD COLUMN`，改为逐列检查 `SELECT COUNT(*) FROM pragma_table_info('integrations') WHERE name=?` 后单列 `ADD COLUMN`；enclosure 摘要索引迁移改为 `return nil`（初始 schema 已建 `(user_id, entry_id, url)` 唯一索引，与 `createEnclosure` 的 `ON CONFLICT` 目标一致）；初始 schema 同步删除 `entries_user_status_changed_idx`，但保留迁移 2 中的 `DROP INDEX IF EXISTS` 以便已存在的库升级 |
| `internal/locale/translations/vi_VN.json` | 新增越南语翻译 | 上游新增文件缺少 `page.about.sqlite_version` 键（fork 独有），已补 `"page.about.sqlite_version": "Phiên bản SQLite:"` |

#### 关键结论

- **SQLite 没有内置 `sha256()`**（实测 `SELECT sha256('abc')` → `SQL logic error: no such function: sha256 (1)`，PostgreSQL 由 pgcrypto 提供）。因此上游任何用 SQL 摘要做常时比较的写法都不能照搬，必须下沉到 Go：`UserByFeverToken` 改为取回 `integrations.fever_token` 后在 Go 侧 `sha256` + `ConstantTimeCompare`。
- **SQLite 不支持一条 `ALTER TABLE` 多列 `ADD COLUMN`**（实测 `SQL logic error: near ",": syntax error (1)`）。多列迁移一律写成循环 + `pragma_table_info` 存在性判断，保证可重复执行。
- 上游 `ca241333` 认为 `entries_user_status_changed_idx` 是 `entries_user_status_changed_published_idx` 的严格前缀，故删除。SQLite 侧同步执行（初始 schema 不再建它）。
- 上游本次大量改动集中在安全加固（SSRF、会话、WebAuthn、cookie 作用域、时序侧信道）与 UI/API 重构，与本 fork 的存储层方言无交叉，冲突面比上次合并（4 个文件）扩大但性质相同：几乎全部集中在存储层 SQL。
- 采用「跟随上游函数签名与逻辑结构、只替换 SQL 方言」的原则，未引入任何 `lib/pq` 依赖。

#### 验证结果

- `go build ./...` 通过；`go vet ./internal/...` 无输出。
- `go test ./internal/...`：`internal/storage`、`internal/database`、`internal/template`、`internal/locale` 全部 ok；仅 `internal/config` 的 `TestParseAdminPasswordFileOptionWithEmptyFile` 失败，原因是沙箱环境无法在 `C:\Windows` 创建临时文件（`Access is denied`），与代码无关。
- 真实库（278 MB、13,019 条 entries）升级验证：`go run main.go -migrate` 输出 `Running database migrations current_version=1 latest_version=4`，成功后 `schema_version=4`，`integrations` 出现 gotify 四列，`entries_user_status_changed_idx` 已消失，`enclosures_user_entry_url_unique_idx` 仍为 `ON enclosures(user_id, entry_id, url)`。
- 全新库验证：`RUN_MIGRATIONS=1 CREATE_ADMIN=1 … go run main.go -migrate` 成功（首次启动出现 `Unable to ping SQLite reader pool; falling back to single connection`，为只读池在库文件刚创建时的正常回退）。

#### 注意事项（本次新发现，后续合并沿用）

1. 上游 SQL 里出现 `sha256()`、`::bytea`、`now()`、`::interval`、`= ANY()`、`DISTINCT ON` 时逐条改写；其中 `sha256()` 与多列 `ADD COLUMN` 无法在 SQLite 侧等价表达，必须改结构（Go 侧计算 / 逐列迁移）。
2. 多列迁移一律写成「查 `pragma_table_info` → 单列 `ADD COLUMN`」的循环，避免重复执行报错，也避免一条语句里的语法差异。
3. 上游删除的冗余索引，若 fork 的初始 schema 里仍存在，要同步删除；但历史迁移中的 `DROP INDEX IF EXISTS` 必须保留，否则已建库无法清理。
4. `go mod tidy` 在本机可能因权限无法直接写 `go.sum`（`Access is denied`），可用 `go mod tidy -modfile=alt.mod` 生成 `alt.sum` 后覆盖，再删除临时文件。
5. `git add` 前确认 `core.autocrlf`（本仓库已设为 `input`），否则工作区 CRLF 会污染索引，导致 diff 出现整文件改动。
6. 本记录的冲突表要区分「真冲突」与「未冲突但需方言改写」，后者更易被漏掉。

