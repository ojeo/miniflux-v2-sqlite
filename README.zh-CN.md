Miniflux 2
==========

Miniflux 是一个极简且立场鲜明的 RSS 阅读器。
它简单、快速、轻量，且极其易于安装。

官方网站：<https://miniflux.app>

> **SQLite 版本**
>
> 本 Fork 在Miniflux 2.3.2基础上 使用 [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite) 替换
> PostgreSQL 为 [SQLite](https://www.sqlite.org/) — 一个纯 Go 驱动，
> **零 CGO 依赖**。整个应用编译为单个静态二进制文件，无需额外数据库依赖。
>
> **关键实现细节**
>
> - **驱动**：`modernc.org/sqlite`（无 CGO），WAL 日志模式，单连接池
>   （MaxOpenConns=1）以避免并发刷新订阅时的 `SQLITE_BUSY` 错误。
> - **连接**：`DATABASE_URL` 接受文件路径（`miniflux.db`）或 `:memory:`；
>   Docker 镜像中默认为 `/var/lib/miniflux/miniflux.db`。
> - **数据库结构**：单版本初始迁移 — 所有表、索引和 FTS5 触发器
>   一步创建完成。
> - **全文搜索**：FTS5 外部内容虚拟表（`entries_fts`），使用 `unicode61`
>   分词器，通过 INSERT/UPDATE/DELETE 触发器保持同步。取消了相关度排序
>   （`ts_rank`），结果按 `published_at` 排序。
> - **数据类型**：`timestamptz` → TEXT（以 Go `time.Time.String()` 格式存储；
>   通过自定义 `model.Time` scanner 读取），`bool` → INTEGER，
>   `text[]` → JSON TEXT，`inet` → TEXT，`jsonb` → TEXT。
> - **SQL 改写**：`$N` → `?N`，`now()` → `strftime()`，
>   `= ANY()` → `IN (...)`，`ILIKE` → `LOWER LIKE`，数据修改 CTE
>   （`WITH ... UPDATE/DELETE ... RETURNING`）拆分为独立语句。
> - **时间处理**：`model.Time.Scan` 解析 8 种时间戳格式，
>   并移除 Go 单调时钟（`m=+...`）和双时区（`+0800 +0800`）后缀。
> - **磁盘空间**：`VACUUM` 在清理任务和 `FlushHistory` 后自动执行，
>   回收已释放的页面。
> - **Docker**：基于 Alpine 的镜像，通过 `entrypoint.sh` 在启动时修正卷权限，
>   然后通过 `su-exec` 降权为 `nobody`（UID 65534）运行。
>
> 参见 **[MIGRATION_SQLITE.md](MIGRATION_SQLITE.md)** 获取完整迁移报告 —
> 文件变更、功能差异、Bug 修复历史、Docker 部署指南和已知限制。



功能特性
--------

### RSS 阅读器

- 支持的订阅格式：Atom 0.3/1.0、RSS 1.0/2.0 和 JSON Feed 1.0/1.1。
- [OPML](https://en.wikipedia.org/wiki/OPML) 文件导入/导出及 URL 导入。
- 支持多种附件（播客、视频、音乐和图片附件）。
- 直接在 Miniflux 内播放 YouTube 视频。
- 使用分类和书签组织文章。
- 公开分享单篇文章。
- 获取网站图标（favicon）。
- 将文章保存到第三方服务。
- 提供全文搜索（基于 SQLite FTS5）。
- 支持 20 种语言：葡萄牙语（巴西）、中文（简体和繁体）、荷兰语、英语（美国）、芬兰语、法语、德语、希腊语、印地语、印尼语、意大利语、日语、波兰语、罗马尼亚语、俄语、台湾白话字、乌克兰语、西班牙语和土耳其语。

### 隐私与安全

- 移除像素追踪器。
- 从 URL 中剥离追踪参数（如 `utm_source`、`utm_medium`、`utm_campaign`、`fbclid` 等）。
- 当订阅来自 FeedBurner 时，获取原始链接。
- 使用 `rel="noopener noreferrer" referrerpolicy="no-referrer"` 属性打开外部链接。
- 实现 HTTP 头 `Referrer-Policy: no-referrer` 以防止 Referrer 泄露。
- 提供媒体代理以规避追踪，并解决 HTTPS 下的混合内容警告。
- 通过隐私保护域名 `youtube-nocookie.com` 播放 YouTube 视频。
- 支持替代 YouTube 播放器，如 [Invidious](https://invidio.us)。
- 阻止外部 JavaScript 以防止追踪并增强安全性。
- 在渲染前对外部内容进行净化处理。
- 强制执行[内容安全策略](https://developer.mozilla.org/en-US/docs/Web/HTTP/CSP)和[可信类型策略](https://developer.mozilla.org/en-US/docs/Web/API/Trusted_Types_API)，仅允许应用自身的 JavaScript，阻止内联脚本和样式。

### 反爬虫机制

- 可选择禁用 HTTP/2 以减少指纹识别。
- 支持自定义 User Agent。
- 支持为特定场景添加自定义 Cookie。
- 支持使用代理以增强隐私或绕过限制。

### 内容处理

- 使用本地 Readability 解析器获取原始文章并提取相关内容。
- 支持基于 <abbr title="Cascading Style Sheets">CSS</abbr> 选择器的自定义抓取规则。
- 支持自定义改写规则进行内容处理。
- 提供正则过滤器，根据特定模式包含或排除文章。
- 可选择允许自签名或无效证书（默认禁用）。
- 抓取 YouTube 网站以获取视频时长作为阅读时间，或使用 YouTube API（默认禁用）。

### 用户界面

- 优化的样式表，适合阅读。
- 响应式设计，无缝适配桌面、平板和移动设备。
- 极简、无干扰的用户界面。
- 无需从 Apple App Store 或 Google Play Store 下载应用。
- 可直接添加到主屏幕以便快速访问。
- 支持丰富的键盘快捷键，提高导航效率。
- 可选的移动端触控手势导航支持。
- 自定义样式表和 JavaScript，按需个性化界面。
- 主题：
    - 浅色（无衬线）
    - 浅色（衬线）
    - 深色（无衬线）
    - 深色（衬线）
    - 系统（无衬线）– 根据系统偏好自动切换深色/浅色主题。
    - 系统（衬线）

### 集成

- 25+ 第三方服务集成：[Apprise](https://github.com/caronc/apprise)、[Betula](https://sr.ht/~bouncepaw/betula/)、[Cubox](https://cubox.cc/)、[Discord](https://discord.com/)、[Espial](https://github.com/jonschoning/espial)、[Instapaper](https://www.instapaper.com/)、[LinkAce](https://www.linkace.org/)、[Linkding](https://github.com/sissbruecker/linkding)、[LinkTaco](https://linktaco.com)、[LinkWarden](https://linkwarden.app/)、[Matrix](https://matrix.org)、[Notion](https://www.notion.com/)、[Ntfy](https://ntfy.sh/)、[Nunux Keeper](https://keeper.nunux.org/)、[Pinboard](https://pinboard.in/)、[Pushover](https://pushover.net)、[RainDrop](https://raindrop.io/)、[Readeck](https://readeck.org/en/)、[Readwise Reader](https://readwise.io/read)、[RssBridge](https://rss-bridge.org/)、[Shaarli](https://github.com/shaarli/Shaarli)、[Shiori](https://github.com/go-shiori/shiori)、[Slack](https://slack.com/)、[Telegram](https://telegram.org)、[Wallabag](https://www.wallabag.org/) 等。
- 小书签（Bookmarklet），可从任何浏览器直接订阅网站。
- Webhook 用于实时通知或自定义集成。
- 兼容使用 Fever 或 Google Reader API 的现有移动应用。
- REST API，提供 [Go](https://github.com/miniflux/v2/tree/main/client) 和 [Python](https://github.com/miniflux/python-client) 客户端库。

### 身份认证

- 本地用户名和密码。
- 通行密钥（[WebAuthn](https://en.wikipedia.org/wiki/WebAuthn)）。
- Google（OAuth2）。
- 通用 OpenID Connect。
- 反向代理认证。

### 技术特性

- 使用 [Go (Golang)](https://golang.org/) 编写。
- 单二进制文件，静态编译无依赖。
- 仅使用 [SQLite](https://www.sqlite.org/)（嵌入式，无需外部服务器）。
- 不依赖任何 ORM 或复杂框架。
- 仅在必要时使用现代原生 JavaScript。
- 所有静态文件通过 Go `embed` 包打包到应用二进制中。
- 支持 Systemd `sd_notify` 协议用于进程监控。
- 使用 Let's Encrypt 自动配置 HTTPS。
- 支持使用自定义 <abbr title="Secure Sockets Layer">SSL</abbr> 证书。
- 启用 TLS 时支持 [HTTP/2](https://en.wikipedia.org/wiki/HTTP/2)。
- 使用内置调度器或传统 cron 作业在后台更新订阅。
- 对图片和 iframe 使用浏览器原生懒加载。
- 仅兼容现代浏览器。
- 遵循 [Twelve-Factor App](https://12factor.net/) 方法论。
- 提供官方 Debian/RPM 包和预编译二进制文件。
- 发布 Docker 镜像至 Docker Hub、GitHub Registry 和 Quay.io Registry，支持 ARM 和 RISC-V 架构。
- 使用有限数量的第三方 Go 依赖。
- 拥有全面的测试套件，包含单元测试和集成测试。
- 即使订阅数百个源，也仅占用几 MB 内存和微不足道的 CPU。
- 遵守/发送 Last-Modified、If-Modified-Since、If-None-Match、Cache-Control、Expires 和 ETags 头，默认轮询间隔为 1 小时。

构建
----

需安装 [Go](https://go.dev/) 和 [Docker](https://www.docker.com/)（用于构建镜像）。

### 编译

| 命令 | 说明 |
|------|------|
| `make miniflux` | 本地编译（PIE） |
| `make linux-amd64` | 交叉编译 Linux amd64 |
| `make linux-arm64` | 交叉编译 Linux arm64 |
| `make linux-armv7` | 交叉编译 Linux armv7 |
| `make linux-riscv64` | 交叉编译 Linux riscv64 |
| `make darwin-amd64` | 交叉编译 macOS amd64 |
| `make darwin-arm64` | 交叉编译 macOS arm64 |
| `make freebsd-amd64` | 交叉编译 FreeBSD amd64 |
| `make openbsd-amd64` | 交叉编译 OpenBSD amd64 |
| `make build` | 编译所有平台 |

### 本地运行

| 命令 | 说明 |
|------|------|
| `make run` | 使用管理员账号运行（`admin` / `test123`） |

### Docker

| 命令 | 说明 |
|------|------|
| `make docker-image` | 构建 Alpine 镜像 |
| `make docker-image-distroless` | 构建 Distroless 镜像 |
| `make docker-images` | 多平台构建并推送 |

> Docker 镜像构建需要已打 tag 的提交（`git tag`）。如需直接使用自定义标签构建：
> ```bash
> docker build --pull -t miniflux/miniflux-sqlite:latest -f packaging/docker/alpine/Dockerfile .
> ```

### 软件包

| 命令 | 说明 |
|------|------|
| `make rpm` | 构建 RPM 包 |
| `make debian` | 构建 deb 包 |
| `make debian-packages` | 构建所有架构 deb 包 |

### 测试与清理

| 命令 | 说明 |
|------|------|
| `make test` | 运行单元测试 |
| `make lint` | 运行代码检查 |
| `make integration-test` | 运行集成测试 |
| `make clean` | 清理构建产物 |

文档
----

Miniflux 文档可在此获取：<https://miniflux.app/docs/>（[手册页](https://miniflux.app/miniflux.1.html)）

- [立场鲜明？](https://miniflux.app/opinionated.html)
- [功能特性](https://miniflux.app/features.html)
- [系统要求](https://miniflux.app/docs/requirements.html)
- [安装指南](https://miniflux.app/docs/installation.html)
- [版本升级](https://miniflux.app/docs/upgrade.html)
- [配置说明](https://miniflux.app/docs/configuration.html)
- [命令行用法](https://miniflux.app/docs/cli.html)
- [用户界面使用](https://miniflux.app/docs/ui.html)
- [键盘快捷键](https://miniflux.app/docs/keyboard_shortcuts.html)
- [外部服务集成](https://miniflux.app/docs/#integrations)
- [改写与抓取规则](https://miniflux.app/docs/rules.html)
- [API 参考](https://miniflux.app/docs/api.html)
- [开发指南](https://miniflux.app/docs/development.html)
- [国际化](https://miniflux.app/docs/i18n.html)
- [常见问题](https://miniflux.app/faq.html)

截图
----

默认主题：

![默认主题](https://miniflux.app/images/overview.png)

使用键盘导航时的深色主题：

![深色主题](https://miniflux.app/images/item-selection-black-theme.png)

致谢
----

- 作者：Frédéric Guillot - [贡献者列表](https://github.com/miniflux/v2/graphs/contributors)
- 遵循 Apache 2.0 许可证分发
