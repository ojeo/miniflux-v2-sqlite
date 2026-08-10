# 上游合并说明文档（Upstream Merge Log）

> **每次发生代码合并（`git merge origin/main`）之前，必须先阅读本文件**，尤其关注下方「合并前必须检查」与「历次合并注意事项」两个章节，再开始操作。

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

---

## 二、合并前必须检查（Checklist）

每次 `git merge origin/main` 之前，按以下顺序检查并记录：

1. **`git fetch origin`** 获取最新上游。
2. **`git merge origin/main`** 开始合并。
3. 对每个冲突文件，判断冲突属于「上游重构（应跟随）」还是「方言差异（应保留 SQLite 版）」：
   - 函数签名/逻辑重构 → 跟随上游（保持与调用方一致）。
   - 存储层 SQL 语法/驱动 → 保留本 fork 的 SQLite 方言。
4. 处理 `go.mod` / `go.sum`：
   - **不要引入** `github.com/lib/pq`（PostgreSQL 驱动）。
   - 保留 `modernc.org/sqlite` 及其间接依赖。
   - 其余依赖版本升级可正常跟随。
   - 合并冲突后运行 **`go mod tidy`** 整理依赖。
5. 验证：**`go build ./...`** 必须通过，且无 lint 错误。
6. 全部通过后 `git add` + `git commit` 完成合并提交。

---

## 三、历次合并记录（Merge Log）

### 2026-08-10 — 合并上游至 `2f9e07bd`（含 v2.3.3）

- **上游范围**：`c4d54f87..2f9e07bd`
- **合并提交数**：10 个
- **新增标签**：`2.3.3`, `v2.3.3`
- **上游提交列表**：
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

---

## 四、通用注意事项（总览）

1. **方言优先**：所有存储层 SQL 一律保持 SQLite 方言（`?N` 占位符）。
2. **跟随重构、保留方言**：函数签名与逻辑重构跟随上游；仅 SQL 语法与驱动选择保留 fork 版。
3. **`lib/pq` 禁止引入**。
4. **用 `go mod tidy` 兜底**，再 `go build ./...` 验证。
5. **每次合并后**：更新本文件，追加一条「历次合并记录」，并复核「合并前必须检查」清单是否有新增注意事项。
