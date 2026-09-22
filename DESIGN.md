# 分布式 PHP 日志中心设计

> 本文档记录已经讨论并确认的设计结果。后续每次讨论结束后同步更新本文档。
>
> 最后更新：2026-09-24

## 1. 项目范围

建设一个集中式日志记录和查看系统，用于接收多个 PHP 站点节点提交的日志，并提供统一存储、检索和 Web 管理后台。

本次讨论范围仅限日志中心本身：

- 定义日志接收接口。
- 定义日志存储结构。
- 定义日志查询和管理能力。
- 不讨论 PHP 站点如何产生日志。
- 不讨论 PHP 日志驱动如何接入。
- 不讨论 PHP 站点的请求边界、IP 获取规则和异常捕获方式。

PHP 站点只需要按照日志中心定义的接口提交数据，`request_ip` 等字段直接使用 PHP 提交的值，日志中心不负责识别或还原。

## 2. 已确定的技术架构

后端采用 Go 单体服务，数据库采用 PostgreSQL。

核心部署只有两个服务：

- `logd`：日志接收、校验、入队、写入和查询服务。
- `PostgreSQL`：日志和元数据存储。

不引入以下外部服务：

- Kafka
- Redis
- Elasticsearch
- 独立的日志采集器
- 独立的微服务网关

Web 管理后台由 `logd` 统一提供，不单独部署前端服务。

```text
PHP 日志提交
    |
    | HTTPS + gzip + JSON 批量请求
    v
logd
    - 鉴权
    - 字段校验
    - 写入内部内存队列
    - 后台批量写入 PostgreSQL
    - 查询 API
    - Web 管理后台
    |
    v
PostgreSQL
```

## 3. 日志提交接口

### 3.1 接口定义

```http
POST /api/v1/logs/batch
Authorization: Bearer <token>
Content-Type: application/json
Content-Encoding: gzip
```

一次请求提交一个批次。批次顶层携带来源节点，避免每条日志重复提交该字段。

### 3.2 请求体

```json
{
  "node_key": "node-sh-03",
  "logs": [
    {
      "event_id": "4f5b4fd5-42b8-47d5-a0db-0273cd76f574",
      "event_time": "2026-09-21T12:30:00.123+08:00",
      "level": "ERROR",
      "request_ip": "1.2.3.4",
      "member": "10001",
      "session_id": "2f3d9f38-1b3d-4e5c-9e74-9dc49b4f2fd0",
      "request_method": "POST",
      "request_url": "https://api.example.com/order/create",
      "request_headers": {},
      "request_params": {},
      "error_scene": "payment.callback.signature_invalid",
      "error_message": "支付回调验签失败",
      "error_file": "/www/shop/app/service/Payment.php",
      "error_line": 126,
      "error_stack": "..."
    }
  ]
}
```

`project_key` 不放在请求体中，由接口 Token 映射得到。一个 Token 对应一个项目。

### 3.3 字段规则

| 字段 | 必填 | 类型 | 限制 |
|---|---:|---|---|
| `node_key` | 是 | string | 1 到 64 字符 |
| `logs` | 是 | array | 1 到 500 条 |
| `event_id` | 是 | UUID | 批次内不能重复 |
| `event_time` | 是 | string | RFC3339，包含时区，且位于允许的接收时间窗口内 |
| `level` | 是 | string | 仅允许 `INFO`、`WARN`、`ERROR`、`DEBUG` |
| `request_ip` | 是 | string | 合法 IPv4 或 IPv6 |
| `member` | 否 | string | 最大 255 字符，用于精确查询 |
| `session_id` | 否 | string | 最大 255 字符，用于精确查询 |
| `request_method` | 是 | string | 最长 16 字符，保存时转为大写 |
| `request_url` | 是 | string | 最大 8192 字符 |
| `request_headers` | 否 | object | 最大 64KB |
| `request_params` | 否 | object | 最大 64KB |
| `error_scene` | 是 | string | 最大 255 字符 |
| `error_message` | 是 | string | 最大 16KB |
| `error_file` | 否 | string | 最大 2048 字符 |
| `error_line` | 否 | integer | 大于 0 |
| `error_stack` | 否 | string | 最大 64KB |

`request_headers` 和 `request_params` 必须是 JSON 对象，不能是 JSON 字符串、数组或标量。

不再单独存储 `request_action`，HTTP 请求方法由 `request_method` 单独记录。

### 3.4 请求处理规则

- 整个批次先完整校验。
- 任意一条日志不合法，整个请求返回 `400`。
- 不支持部分成功。
- 解压后请求体最大为 5MB。
- `level` 只接受 `INFO`、`WARN`、`ERROR`、`DEBUG`，统一使用大写。
- `event_time` 不能晚于日志中心当前时间 5 分钟。
- `ERROR` 的 `event_time` 不能早于当前时间 31 天。
- `INFO`、`WARN`、`DEBUG` 的 `event_time` 不能早于当前时间 `retention_days` 天。
- `event_time` 超出接收窗口时返回 `400`，错误字段为 `event_time`。
- 成功接收后返回 `202`。
- `202` 表示日志已经进入日志中心的内部内存队列，不表示 PostgreSQL 已经提交。

### 3.5 成功响应

```json
{
  "accepted": 100
}
```

### 3.6 错误响应

```json
{
  "error": {
    "code": "INVALID_LOG",
    "message": "event_time is invalid",
    "index": 12,
    "field": "event_time"
  }
}
```

### 3.7 状态码

| 状态码 | 含义 |
|---|---|
| `202` | 整个批次接收成功 |
| `400` | JSON 格式错误或字段校验失败 |
| `401` | Token 缺失或无效 |
| `403` | 项目被禁用 |
| `413` | 解压后请求体超过 5MB |
| `503` | 接收队列已满 |
| `500` | 服务内部错误 |

## 4. PostgreSQL 存储设计

### 4.1 日志表

```sql
CREATE TABLE log_events (
    event_id         uuid        NOT NULL,
    event_time       timestamptz NOT NULL,
    received_at      timestamptz NOT NULL DEFAULT now(),
    level            text        NOT NULL
        CHECK (level IN ('INFO', 'WARN', 'ERROR', 'DEBUG')),

    project_key      text        NOT NULL,
    node_key         text        NOT NULL,
    locked           boolean     NOT NULL DEFAULT false,

    request_ip       inet        NOT NULL,
    member           text        NOT NULL DEFAULT '',
    session_id       text        NOT NULL DEFAULT '',
    request_method   text        NOT NULL,
    request_url      text        NOT NULL,
    request_headers  jsonb,
    request_params   jsonb,

    error_scene      text        NOT NULL,
    error_message    text        NOT NULL,
    error_file       text,
    error_line       integer,
    error_stack      text,

    PRIMARY KEY (level, event_time, event_id)
) PARTITION BY LIST (level);

CREATE TABLE log_events_error
    PARTITION OF log_events
    FOR VALUES IN ('ERROR')
    PARTITION BY RANGE (event_time);

CREATE TABLE log_events_other
    PARTITION OF log_events
    FOR VALUES IN ('INFO', 'WARN', 'DEBUG')
    PARTITION BY RANGE (event_time);
```

`ERROR` 使用按月创建的子分区，不参与自动清理。`INFO`、`WARN`、`DEBUG` 使用按天创建的子分区，按保留天数清理。

### 4.2 日志字段来源

| 数据库字段 | 来源 |
|---|---|
| `event_id` | PHP 请求体 |
| `event_time` | PHP 请求体 |
| `received_at` | 日志中心生成 |
| `level` | PHP 请求体 |
| `project_key` | 接口 Token 映射 |
| `node_key` | 批次顶层字段 |
| `request_ip` | PHP 请求体 |
| `member` | PHP 请求体，可选 |
| `session_id` | PHP 请求体，可选 |
| `request_method` | PHP 请求体 |
| `request_url` | PHP 请求体 |
| `request_headers` | PHP 请求体 |
| `request_params` | PHP 请求体 |
| `error_scene` | PHP 请求体 |
| `error_message` | PHP 请求体 |
| `error_file` | PHP 请求体 |
| `error_line` | PHP 请求体 |
| `error_stack` | PHP 请求体 |

### 4.3 日志目录维度表

日志目录不再每次扫描 `log_events`，而是从 `log_dimensions` 读取。

```sql
CREATE TABLE log_dimensions (
    project_key  text        NOT NULL,
    node_key     text        NOT NULL,
    level        text        NOT NULL
        CHECK (level IN ('INFO', 'WARN', 'ERROR', 'DEBUG')),
    last_seen_at timestamptz NOT NULL,

    PRIMARY KEY (project_key, node_key, level)
);
```

writer 在日志批次事务内按 `(project_key, node_key, level)` 去重，并使用 `INSERT ... ON CONFLICT DO UPDATE` 更新 `last_seen_at`。目录项按历史出现记录保留，删除日志后不会立即从左侧目录移除。

### 4.4 管理数据表

项目 Token 只在创建时返回一次明文，数据库只保存 SHA-256 哈希。

```sql
CREATE TABLE project_tokens (
    id          uuid        PRIMARY KEY,
    project_key text        NOT NULL,
    name        text        NOT NULL,
    token_hash  text        NOT NULL UNIQUE,
    enabled     boolean     NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE system_settings (
    setting_key   text        PRIMARY KEY,
    setting_value jsonb       NOT NULL,
    updated_at    timestamptz NOT NULL DEFAULT now()
);
```

初始系统设置：

| setting_key | 默认值 | 说明 |
|---|---|---|
| `retention_days` | `30` | `INFO`、`WARN`、`DEBUG` 日志保留天数 |

### 4.5 索引

初始只建立以下索引：

```sql
CREATE INDEX idx_log_events_time
    ON log_events USING brin (event_time);

CREATE INDEX idx_log_events_time_page
    ON log_events (event_time DESC, event_id DESC);

CREATE INDEX idx_log_events_project_time
    ON log_events (project_key, event_time DESC, event_id DESC);

CREATE INDEX idx_log_events_node_time
    ON log_events (node_key, event_time DESC, event_id DESC);

CREATE INDEX idx_log_events_scene_time
    ON log_events (error_scene, event_time DESC, event_id DESC);

CREATE INDEX idx_log_events_ip_time
    ON log_events (request_ip, event_time DESC, event_id DESC);

CREATE INDEX idx_log_events_member_time
    ON log_events (member, event_time DESC, event_id DESC);

CREATE INDEX idx_log_events_session_id_time
    ON log_events (session_id, event_time DESC, event_id DESC);
```

初始不对 `request_url`、`request_headers`、`request_params`、`error_message` 和 `error_stack` 建立全文索引或 GIN 索引。

`level` 是顶层分区键，查询指定日志级别时可以直接做分区裁剪，不需要单独建立 `level` 索引。

### 4.6 分区和保留策略

- 日志表先按 `level` 做列表分区。
- `ERROR` 单独分区，再按月按 `event_time` 做范围子分区。
- `INFO`、`WARN`、`DEBUG` 共用一个非错误分区，再按天按 `event_time` 做范围子分区。
- `ERROR` 日志永久保存，不进行自动清理。
- 其他级别默认保留 30 天，通过 `system_settings.retention_days` 调整。
- 清理时直接删除过期的非错误每日子分区，不逐行删除。
- 非错误日志按 `Asia/Shanghai` 的自然日切分分区，接收窗口按当前时间向前 `retention_days` 天滚动计算。
- `retention_days` 最小为 1。
- `ERROR` 分区预创建前一个月、当前月和下一个月的分区。
- 非错误分区预创建从接收窗口起始时刻所在日期到明天为止的全部日期分区。
- 清理非错误分区时，删除结束时间早于或等于接收窗口起始时刻的日期分区。
- 修改 `retention_days` 后，先确保新增保留窗口内的日期分区存在，再应用新设置。
- 分区维护任务每小时运行一次，按当前设置补齐所需分区并清理过期非错误分区。

### 4.7 接收队列

V1 使用有界内存队列，不增加本地磁盘 WAL。

- 默认队列容量为 50000 条事件，容量可配置。
- 接收接口完成校验后写入队列，成功入队后返回 `202`。
- 队列已满时返回 `503`。
- 服务重启或崩溃时，队列中尚未写入 PostgreSQL 的日志会丢失。
- V1 不提供零丢失保证。

该方案优先减少组件和代码量。后续如果要求进程崩溃后继续保留未落库日志，再把内存队列替换为本地磁盘 WAL。

### 4.8 PostgreSQL 写入规则

`logd` 默认启动一个后台写入 worker。

- 每批最多 1000 条事件。
- 距离上一批最多 1 秒立即触发写入。
- 每个批次使用一个 PostgreSQL 事务。
- 写入失败时使用指数退避重试，初始等待 200ms，最大等待 5 秒。
- 持续写入失败时队列最终会被填满，接收接口开始返回 `503`。
- V1 不增加死信队列。
- `logd` 启动时确保当前和下一个错误日志月份分区、当前和下一个非错误日志日期分区存在。
- 服务正常退出时最多尝试排空队列 10 秒。

使用事件 ID 做幂等处理：

```sql
ON CONFLICT (level, event_time, event_id) DO NOTHING
```

同一事件重复提交不会产生重复记录。

## 5. Web 管理后台

管理后台需要支持以下基础能力：

- 按项目筛选。
- 按节点筛选。
- 按日志级别筛选。
- 按错误场景筛选。
- 按用户筛选。
- 按 Session ID 筛选。
- 按时间范围筛选。
- 查看日志详情。
- 查看请求 URL。
- 查看请求 Header 和参数。
- 查看错误信息、错误文件和堆栈。

### 5.1 日志列表接口

```http
GET /api/v1/logs
```

查询参数：

| 参数 | 必填 | 说明 |
|---|---:|---|
| `start_time` | 是 | 开始时间，包含该时间 |
| `end_time` | 是 | 结束时间，不包含该时间 |
| `project_key` | 否 | 精确匹配 |
| `node_key` | 否 | 包含匹配（不区分大小写） |
| `level` | 否 | 精确匹配，值为 `INFO`、`WARN`、`ERROR`、`DEBUG` |
| `error_scene` | 否 | 包含匹配（不区分大小写） |
| `request_ip` | 否 | 精确匹配 |
| `member` | 否 | 精确匹配 |
| `session_id` | 否 | 精确匹配 |
| `limit` | 否 | 默认 50，最大 200 |
| `cursor` | 否 | 上一页返回的分页游标 |

约束：

- `start_time` 和 `end_time` 必填，用于分区裁剪。
- 单次查询的最大时间跨度为 31 天。
- 结果按照 `event_time DESC, event_id DESC` 排序。
- 使用游标分页，不使用 `OFFSET`。
- 列表接口不返回 `request_headers`、`request_params`、`error_stack` 等大字段。
- 仅提供必要字段的精确查询，不提供 `error_message` 和 `error_stack` 的模糊搜索。

响应示例：

```json
{
  "items": [
    {
      "event_id": "4f5b4fd5-42b8-47d5-a0db-0273cd76f574",
      "event_time": "2026-09-21T12:30:00.123+08:00",
      "received_at": "2026-09-21T12:30:00.456+08:00",
      "project_key": "shop-api",
      "node_key": "node-sh-03",
      "level": "ERROR",
      "request_ip": "1.2.3.4",
      "member": "10001",
      "session_id": "2f3d9f38-1b3d-4e5c-9e74-9dc49b4f2fd0",
      "request_url": "https://api.example.com/order/create",
      "error_scene": "payment.callback.signature_invalid",
      "error_message": "支付回调验签失败"
    }
  ],
  "next_cursor": "b3BhcXVlLWN1cnNvcg",
  "has_more": true
}
```

列表接口不返回总记录数。统计数量需要使用单独的统计接口，避免每次列表查询都对大分区执行 `COUNT`。

### 5.2 日志详情接口

```http
GET /api/v1/logs/{event_id}?level=ERROR&event_time=2026-09-21T12:30:00.123%2B08:00
```

`level` 和 `event_time` 是分区键，必须和 `event_id` 一起提交，避免查询所有历史分区。

详情接口返回完整字段：

- 列表接口的全部字段。
- `request_headers`
- `request_params`
- `error_file`
- `error_line`
- `error_stack`

### 5.3 分页规则

游标使用不透明字符串，内部包含上一页最后一条记录的：

```text
event_time
event_id
```

下一页查询使用：

```sql
WHERE (event_time, event_id) < (:cursor_event_time, :cursor_event_id)
ORDER BY event_time DESC, event_id DESC
```

游标格式属于实现细节，不保证跨版本兼容。

### 5.4 查询接口状态码

| 状态码 | 含义 |
|---|---|
| `200` | 查询成功 |
| `400` | 参数缺失或格式错误 |
| `401` | 管理后台未登录或凭证无效 |
| `500` | 服务内部错误 |

日志列表和详情接口使用管理后台凭证，不复用日志提交接口的项目 Token。

### 5.5 后台登录

后台不建立用户表和权限模型，只使用一个访问密码。

```http
POST /api/v1/admin/login
POST /api/v1/admin/logout
```

登录成功后设置 12 小时有效期的 HttpOnly 会话 Cookie。所有管理接口都要求有效会话。

管理员密码使用配置文件中的 `admin.password` 配置，程序启动时转换为 bcrypt 哈希。会话 Cookie 使用 `admin.session_secret` 签名，该值至少 32 个字符。数据库中不保存后台用户。

### 5.6 项目 Token 管理

后台提供项目 Token 的列表、新增、修改和删除功能。

```http
GET    /api/v1/admin/project-tokens
POST   /api/v1/admin/project-tokens
PATCH  /api/v1/admin/project-tokens/{id}
DELETE /api/v1/admin/project-tokens/{id}
```

规则：

- 创建 Token 时提交 `project_key`、`name` 和初始 `enabled` 状态。
- 服务端生成 32 字节随机 Token。
- Token 明文只在创建响应中返回一次。
- 数据库只保存 Token 哈希。
- 修改操作允许调整 `project_key`、`name` 和 `enabled`。
- 删除操作为物理删除 Token。
- `logd` 在内存中缓存项目 Token 映射，后台增删改后立即刷新缓存。

### 5.7 系统设置

```http
GET   /api/v1/admin/settings
PATCH /api/v1/admin/settings
```

V1 至少支持以下设置：

- `retention_days`：非错误日志保留天数，默认 30。

不提供 Header 和参数脱敏设置。

### 5.8 logd 自身监控、指标和告警

V1 不引入 Prometheus Server、Grafana 或 Alertmanager，只由 `logd` 自身提供以下能力：

```http
GET /healthz
GET /readyz
GET /metrics
```

- `/healthz`：检查进程是否存活。
- `/readyz`：检查数据库连接和分区初始化是否正常。
- `/metrics`：输出 Prometheus 文本格式指标。
- 后台提供系统状态页面。
- `logd` 自身日志使用 JSON 格式输出到 stdout 和 stderr，由 Supervisor 收集。

初始指标：

| 指标 | 说明 |
|---|---|
| `logd_ingest_requests_total{status}` | 接收接口请求数 |
| `logd_ingest_events_total{level}` | 接收事件数 |
| `logd_queue_depth` | 当前队列深度 |
| `logd_queue_capacity` | 队列容量 |
| `logd_queue_rejected_total` | 队列满拒绝数 |
| `logd_db_write_batches_total{result}` | PostgreSQL 批次写入数 |
| `logd_db_write_events_total{result}` | PostgreSQL 事件写入数 |
| `logd_db_write_duration_seconds` | PostgreSQL 写入耗时 |
| `logd_last_successful_db_write_timestamp_seconds` | 最近一次成功写入时间 |

告警规则：

| 告警 | 条件 |
|---|---|
| PostgreSQL 不可用 | `/readyz` 连续失败 2 分钟 |
| 队列接近上限 | 队列使用率超过 80%，持续 5 分钟 |
| 长时间未成功写入 | 队列非空且连续 2 分钟没有成功写入 |
| 接收错误率过高 | 连续 5 分钟请求错误率超过 5% |

告警只显示在后台状态页和 stdout JSON 日志中。

## 6. 当前明确排除的内容

- 不讨论 PHP 站点如何生成 IP、Header、参数和异常堆栈。
- 不讨论 ThinkPHP5 日志驱动的实现。
- 不讨论 PHP 站点调用日志接口的方式。
- 不讨论 PHP 站点的超时、重试和失败处理。
- 日志中心不主动识别 `request_ip`，只保存 PHP 提交的值。
- 不实现 Header 和参数脱敏。
- 不实现消息正文和错误堆栈的模糊搜索。
- 不实现 PostgreSQL 备份。
- 不引入用户、角色和细粒度权限模型。

## 7. 部署方式

V1 使用本机 Supervisor 直接运行 `logd`，不使用 Docker 和 Kubernetes。PostgreSQL 作为本机数据库服务运行。

Supervisor 配置基线：

```ini
[program:logd]
command=/opt/logd/logd -config /etc/logd/config.yml
directory=/opt/logd
autostart=true
autorestart=true
startsecs=5
stopwaitsecs=15
stdout_logfile=/var/log/logd/logd.stdout.log
stderr_logfile=/var/log/logd/logd.stderr.log
```

`stopwaitsecs` 必须大于 `logd` 的 10 秒队列排空时间。`logd` 自身日志使用 JSON 格式输出，由 Supervisor 收集。

### 7.1 PostgreSQL 连接配置

`logd` 只通过配置文件获取 PostgreSQL 连接信息。生产环境使用独立的最小权限数据库账号，不使用 `postgres` 超级用户。连接地址、认证方式和是否启用 TLS 由实际部署环境决定，不写入应用逻辑。

当前开发环境使用 PostgreSQL 18.6，数据库为 `logd`，程序使用独立角色 `logd`，该角色拥有数据库并负责迁移和分区管理。数据库密码只保存在运行配置文件中，不写入设计文档。

## 8. 开发实施方案

### 8.1 交付物

V1 交付以下内容：

- 一个 `logd` 可执行文件，包含接收 API、写入程序、查询 API、管理 API 和 Web 管理后台。
- 一组 PostgreSQL 初始化 SQL 和版本迁移 SQL。
- 一份 `config.yml` 配置示例。
- 一份 Supervisor 配置示例。
- 一份部署和首次初始化说明。

不增加独立迁移工具、前端构建服务或管理后台服务。

### 8.2 代码结构

建议使用以下目录结构：

```text
cmd/logd/                 程序入口
internal/config/          配置加载和校验
internal/server/          HTTP 路由、中间件和响应
internal/ingest/          日志接口校验和入队
internal/queue/           有界内存队列
internal/store/           PostgreSQL 访问、事务和迁移
internal/partition/       分区创建和清理
internal/admin/           登录、Token、查询、设置和状态
internal/monitor/         指标、状态和告警
internal/web/             可嵌入二进制的后台页面和静态资源
migrations/               PostgreSQL SQL 文件
```

后端使用 Go 1.27 和标准库 `net/http`，路由使用 `http.ServeMux`，不引入 Gin、Echo、Fiber、Chi 等 Web 框架。鉴权、请求日志、恢复、超时和中间件组合使用少量自定义 `http.Handler` 包装实现。

数据访问直接使用 `pgx/v5` 和 `pgxpool`，不使用 ORM 或查询构建器。分区裁剪、动态分区管理和批量写入需要明确控制 SQL，直接访问驱动更合适。

其他依赖限定为：

- `gopkg.in/yaml.v3`：读取 `config.yml`。
- `golang.org/x/crypto/bcrypt`：校验管理员密码哈希。
- 标准库 `html/template` 和 `embed`：生成后台页面并嵌入静态资源。
- `/metrics` 直接输出 Prometheus 文本格式，不引入 Prometheus 客户端库。

管理后台使用服务端模板、少量原生 JavaScript 和本地静态 CSS，不引入 Node.js 构建流程或前端框架。

### 8.2.1 Web 管理后台实现方式

V1 管理后台只包含登录、日志列表、日志详情、项目 Token、系统设置和系统状态页面，使用服务端渲染即可覆盖，不需要 SPA。

页面路由：

```text
GET /admin/login
GET /admin/logs
GET /admin/logs/{event_id}
GET /admin/project-tokens
GET /admin/settings
GET /admin/status
GET /assets/*
```

实现规则：

- 页面使用 `html/template` 渲染，公共导航和布局通过模板组合复用。
- 筛选条件和分页游标保存在 URL 查询参数中，刷新和浏览器前进后退不会丢失状态。
- 页面处理器和 JSON API 共用服务层及数据访问层，不复制查询逻辑。
- 原生 JavaScript 只负责确认操作、显示 Token 明文、复制文本、刷新状态和日志列表无限滚动，不承担页面路由和复杂状态管理。
- 日志列表不再显示上一页/下一页按钮，滚动到列表底部后自动请求并追加下一页；顶部显示当前已加载条数。
- CSS 和 JavaScript 作为本地静态文件嵌入二进制，不依赖 CDN。
- 登录会话使用带签名的 HttpOnly Cookie，修改数据的请求增加 CSRF 校验。
- 返回页面的请求统一增加 `Content-Security-Policy`、`X-Content-Type-Options` 和 `X-Frame-Options` 等安全响应头。

如果后续需要复杂图表，可以在页面中增加单个本地静态 JavaScript 库，但仍不引入 Node.js 构建流程或前端应用框架。

### 8.3 启动和退出顺序

正常启动顺序：

1. 加载并校验配置。
2. 建立 PostgreSQL 连接池并检查连接。
3. 执行内嵌数据库迁移。
4. 加载项目 Token 和系统设置缓存。
5. 按当前设置确保接收窗口内全部日志分区存在。
6. 启动写入 worker、分区维护任务和告警任务。
7. 开始监听 HTTP 端口。

正常退出顺序：

1. 停止接收新请求。
2. 最多等待 10 秒排空写入队列。
3. 停止后台任务。
4. 关闭 PostgreSQL 连接池。

### 8.4 数据库初始化和迁移

- SQL 文件编译进 `logd`，使用 `schema_migrations` 表记录已执行版本。
- 每个迁移在单独事务中执行；迁移失败时终止启动。
- 首次迁移创建最终态 `log_events`、分区根表、索引、`project_tokens`、`system_settings` 和 `log_dimensions`。
- 首次启动不由程序自动创建项目 Token，管理员登录后台后手动创建。
- 后台密码和会话签名密钥通过配置文件中的 `admin` 配置段提供，不通过环境变量注入。

### 8.5 核心处理流程

日志接收流程：

1. 校验 Bearer Token，并解析对应的 `project_key`。
2. 限制压缩请求和解压后大小。
3. 完整校验批次和全部日志。
4. 将全部事件一次性写入有界内存队列。
5. 成功返回 `202`；入队失败返回 `503`。

日志写入流程：

1. writer 按最多 1000 条或最多等待 1 秒获取事件。
2. 每个批次开启一个 PostgreSQL 事务。
3. 使用 `ON CONFLICT DO NOTHING` 写入日志，并同步维护 `log_dimensions` 后提交。
4. 失败时保留当前批次并按 200ms 到 5 秒指数退避重试。
5. 队列持续增长到上限后，接收接口返回 `503`。

日志查询流程：

1. 校验管理后台会话。
2. 校验时间范围和筛选字段。
3. 根据时间范围和 `level` 优先进行分区裁剪。
4. 使用 `(event_time DESC, event_id DESC)` 游标分页。
5. 列表只返回摘要字段，详情按分区键精确读取大字段。

### 8.6 开发顺序

1. 初始化 Go 项目、配置模块、HTTP 基础路由和健康检查。
2. 完成数据库迁移框架、基础表和分区管理。
3. 完成项目 Token 鉴权、批次校验和内存队列。
4. 完成 PostgreSQL 批量写入、幂等、重试和优雅退出。
5. 完成后台登录、Token 管理、系统设置和查询 API。
6. 完成日志列表、详情、设置和系统状态页面。
7. 完成指标、告警、Supervisor 配置和部署说明。
8. 按验收清单进行集成验证。

每一步完成后都保证程序可以编译和启动，再进入下一步，避免同时铺设全部模块后再集中排错。

### 8.7 首版验收清单

- 合法批次返回 `202`，并在 1 秒内写入 PostgreSQL。
- 任意一条日志非法时整个批次返回 `400`，且没有部分入库。
- 无效 Token 返回 `401`，被禁用项目返回 `403`。
- 队列满时返回 `503`，服务不崩溃。
- 相同事件重复提交只保留一条记录。
- `ERROR` 分区不会被保留任务清理。
- 非错误日志超过保留天数后，通过删除整个日期分区完成清理。
- 列表接口支持级别、项目、节点、错误场景、IP 和时间范围筛选。
- 列表分页使用游标，滚动加载过程中不产生重复或漏项。
- 详情接口能够读取 Header、参数、错误文件和错误堆栈。
- 后台密码、Token、系统设置和状态页面工作正常。
- `/metrics` 返回包含接收、队列、写入和最近成功写入时间的 Prometheus 文本指标。
- 告警状态在状态页面和状态 API 中可见。
- Supervisor 停止服务时，`logd` 有机会完成最多 10 秒的队列排空。

### 8.8 event_time 接收窗口

日志中心按实时日志设计，不承担历史日志回填：

- 所有日志的 `event_time` 最晚只能比当前时间快 5 分钟。
- `ERROR` 最多允许延迟 31 天，超出后整个批次返回 `400`。
- `INFO`、`WARN`、`DEBUG` 最多允许延迟 `retention_days` 天，超出后整个批次返回 `400`。
- 分区维护使用 `Asia/Shanghai` 计算自然日和月份边界。
- 接收窗口内需要的分区必须在 HTTP 服务开始监听前创建完成。

该策略保证写入 worker 不会因为缺少历史分区而进入无限重试，也避免系统被用于导入任意历史日志。

### 8.9 API 接口文档

接口清单、字段约束、请求响应示例、鉴权方式和错误码统一维护在 [`API.md`](API.md)。

可导入 API 调试软件的 OpenAPI 3.0.3 文件维护在 [`openapi.yaml`](openapi.yaml)。

`API.md` 以当前代码实现为准，包含：

- PHP 日志批量提交接口。
- 健康检查、就绪检查和 Prometheus 指标接口。
- 管理后台登录和退出接口。
- 日志列表和详情查询接口。
- 项目 Token 管理接口。
- 系统设置和系统状态接口。
- Web 管理后台页面路由清单。

后续接口变更时同步更新 `API.md` 和本文档中的设计约束。

### 8.10 PHP 站点接入提示词

PHP 站点侧接入提示词统一维护在 [`PHP_INTEGRATION_PROMPT.md`](PHP_INTEGRATION_PROMPT.md)。

该提示词要求 PHP 项目先读取现有 ThinkPHP 5 日志驱动、配置和异常处理，再新增同步远程日志驱动。提示词包含接口地址、Token 配置、字段映射、错误处理、HTTP 超时和集成验收要求，不在提示词中内置真实 Token。

## 9. 尚未确定的事项

- 是否启用 HTTPS，以及 TLS 证书的配置方式。
- 是否需要日志数量统计 API。

## 10. 变更记录

### 2026-09-21

- 确定采用 Go 单体服务加 PostgreSQL。
- 确定不引入 Kafka、Redis、Elasticsearch 和独立采集器。
- 确定日志通过 HTTP JSON 批量提交。
- 确定接收接口为 `POST /api/v1/logs/batch`。
- 确定 `project_key` 由 Token 映射，不由 PHP 提交。
- 确定 `node_key` 放在批次顶层。
- 确定删除 `request_action`，后于第十八轮新增独立的 `request_method` 字段。
- 确定 `request_ip` 是 PHP 提交字段，日志中心不负责识别。
- 确定请求体大小、字段限制、状态码和批次全部校验规则。
- 确定成功返回 `202`，表示日志已进入内部内存队列。
- 确定 PostgreSQL 日志表结构、初始索引和分区方向。

### 2026-09-21 第二轮

- 确定 V1 使用有界内存队列，不增加本地磁盘 WAL。
- 确定默认队列容量为 50000 条事件，队列满时返回 `503`。
- 确定服务重启或崩溃时允许丢失尚未写入 PostgreSQL 的队列数据。
- 确定后台默认使用一个写入 worker，每批最多 1000 条或等待 1 秒。
- 确定每个批次使用一个 PostgreSQL 事务并支持指数退避重试。
- 确定启动时自动确保当前月份和下一个月份分区存在。
- 确定正常退出时最多排空队列 10 秒。
- 确定列表和详情分离的查询接口。
- 确定 `GET /api/v1/logs` 和 `GET /api/v1/logs/{event_id}?event_time=...`。
- 确定查询必须指定时间范围，单次最大跨度为 31 天。
- 确定使用 `event_time DESC, event_id DESC` 的游标分页，不使用 `OFFSET`。
- 确定列表接口不返回 Header、参数和堆栈，详情接口返回完整数据。
- 确定查询和详情接口不复用日志提交接口的项目 Token。
- 确定索引增加 `event_id DESC`，以支持稳定的游标分页。

### 2026-09-21 第三轮

- 确定日志级别为 `INFO`、`WARN`、`ERROR`、`DEBUG`。
- 确定 `ERROR` 日志永久保存。
- 确定其他日志默认保留 30 天，并通过系统设置调整。
- 确定日志表先按 `level` 分区，`ERROR` 按月子分区且不清理，其他级别按天子分区并清理。
- 确定 PostgreSQL 幂等键为 `(level, event_time, event_id)`。
- 确定后台提供项目 Token 的列表、新增、修改和删除。
- 确定 Token 明文只返回一次，数据库只保存哈希。
- 确定后台只使用一个访问密码，不建立用户、角色和权限模型。
- 确定后台密码使用配置文件的 `admin.password`，会话签名密钥使用 `admin.session_secret`。
- 确定查询接口增加 `level` 精确筛选。
- 确定不实现消息正文和错误堆栈的模糊搜索。
- 确定不实现 Header 和参数脱敏。
- 确定不实现 PostgreSQL 备份。
- 确定 `logd` 提供 `/healthz`、`/readyz` 和 `/metrics`。
- 确定 `logd` 提供系统状态页面、JSON 自身日志和告警状态展示。
- 确定 V1 使用 Supervisor 直接运行 `logd`，不使用容器编排。

### 2026-09-21 第四轮

- 确定 V1 交付可执行文件、SQL 迁移、配置示例、Supervisor 示例和部署说明。
- 确定代码目录按配置、HTTP、接收、队列、存储、分区、后台和监控拆分。
- 确定 Go 标准库 HTTP 服务、`pgx/v5`、服务端模板和本地静态资源的技术基线。
- 确定启动时执行数据库迁移和分区初始化，迁移失败时终止启动。
- 确定管理员密码和会话密钥只通过配置文件提供。
- 确定从基础设施、接收链路、写入链路、管理接口、后台页面到运维配置的开发顺序。
- 确定首版集成验收清单。
- 将 PostgreSQL 连接参数归入部署配置，移除已失效的连接排障内容。

### 2026-09-21 第五轮

- 确定日志系统只接收实时日志，不承担历史日志回填。
- 确定 `event_time` 最晚只能比当前时间快 5 分钟。
- 确定 `ERROR` 最多允许延迟 31 天，超出范围时拒绝整个批次。
- 确定非错误日志最多允许延迟 `retention_days` 天，超出范围时拒绝整个批次。
- 确定按 `Asia/Shanghai` 计算非错误日志的自然日分区边界，接收窗口按向前 `retention_days` 天的滚动时间计算。
- 确定启动和分区维护任务预创建整个接收窗口所需分区，避免写入时动态补建历史分区。
- 确定提高 `retention_days` 时先补齐日期分区，再应用新设置。

### 2026-09-21 第六轮

- 确定项目使用 Go 1.27。
- 确定不引入 Web 框架，HTTP 路由直接使用标准库 `http.ServeMux`。
- 确定中间件使用自定义 `http.Handler` 包装实现。
- 确定 PostgreSQL 访问只使用 `pgx/v5` 和 `pgxpool`，不引入 ORM 或查询构建器。
- 确定配置、密码哈希、指标、模板和静态资源的具体依赖范围。

### 2026-09-21 第七轮

- 确定 Web 管理后台使用服务端渲染，不实现 SPA。
- 确定页面采用 `html/template`、本地静态 CSS 和少量原生 JavaScript。
- 确定筛选和分页状态保存在 URL 查询参数中。
- 确定页面处理器和 JSON API 共用服务层及数据访问层。
- 确定管理员会话使用签名 HttpOnly Cookie，并增加 CSRF 校验和安全响应头。
- 确定静态资源嵌入二进制，不依赖 CDN、Node.js 或前端框架。

### 2026-09-21 第八轮

- 将开发环境升级为 Go 1.27.1。
- 确认 PostgreSQL 18.6 可连接，并创建 `logd` 数据库。
- 创建独立数据库角色 `logd` 并使其拥有 `logd` 数据库。
- 完成 Go 项目骨架、YAML 配置、PostgreSQL 连接池和版本迁移。
- 完成基础表、错误月分区、非错误日分区以及过期分区清理逻辑。
- 完成 `/healthz` 和 `/readyz` 健康检查。
- 使用真实 PostgreSQL 验证迁移、3 个错误月分区、32 个非错误日分区和默认保留设置。

### 2026-09-21 第九轮

- 完成有界内存队列，按事件数原子预留容量，队列满时整批拒绝。
- 完成项目 Token 加载和内存缓存，使用 SHA-256 十六进制哈希匹配数据库中的 Token。
- 完成 `POST /api/v1/logs/batch` 接收接口，支持 gzip 和未压缩请求体。
- 完成批次、字段、UUID、IP、JSON 对象、时间和保留窗口校验。
- 完成每个请求完整校验后一次性入队，不支持部分成功。
- 完成 writer 每批最多 1000 条或等待 1 秒、单事务批量写入和 `ON CONFLICT DO NOTHING`。
- 完成写入失败的 200ms 到 5 秒指数退避重试。
- 完成正常退出时停止 HTTP 接收、关闭队列并最多等待 10 秒排空 writer。
- 使用真实 PostgreSQL 验证合法 gzip 批次返回 `202`，INFO 写入日分区，ERROR 写入月分区，Header、参数、IPv6、错误文件和堆栈均正确落库。
- 验证无效 Token 返回 `401`，无效级别、时间和 UUID 返回 `400`。

### 2026-09-21 第十轮

- 完成后台访问密码登录、bcrypt 校验、12 小时签名 HttpOnly Cookie 会话。
- 完成管理页面和 JSON API 的会话鉴权，以及修改类请求的 CSRF 校验。
- 完成日志列表、详情、筛选和基于 `(event_time, event_id)` 的游标分页。
- 完成项目 Token 列表、新增、修改、删除 API 和后台页面，变更后立即刷新内存缓存。
- 完成系统设置 API 和页面；保留天数变更时同步补齐分区、清理过期分区并更新接收校验窗口。
- 完成系统状态 API 和页面，展示数据库状态、队列深度、队列容量、保留天数和最近写入时间。
- 完成每小时分区维护任务。
- 完成服务端模板、本地 CSS 和原生 JavaScript 后台，不引入 Node.js 构建流程。
- 使用真实 PostgreSQL 验证登录、Token 创建、日志提交、列表、详情、Token 即时禁用和删除、保留天数变更及后台页面渲染。
- 更新 Supervisor 示例，移除管理员密码和会话密钥的环境变量配置。

### 2026-09-21 第十一轮

- 完成 Prometheus 文本格式 `/metrics`，输出接收请求、接收事件、队列状态、写入批次、写入事件、写入耗时和最近成功写入时间。
- 指标实现只使用 Go 标准库，不引入 Prometheus 客户端依赖。
- 完成 PostgreSQL 不可用、队列使用率过高、写入停滞和接收错误率过高的告警判断。
- 完成告警状态展示和结构化告警日志。
- 后台状态页面和状态 API 增加当前告警列表。
- 使用真实 PostgreSQL 验证 `/healthz`、`/readyz`、`/metrics`、登录、临时 Token 创建、日志接收、指标增长和状态页面渲染。

### 2026-09-22 第十二轮

- 确定后台访问密码和会话签名密钥改为配置文件中的 `admin` 配置段。
- 确定不再使用 `ADMIN_PASSWORD_HASH` 和 `SESSION_SECRET` 环境变量。
- 确定 `admin.password` 在程序启动时转换为 bcrypt 哈希，`admin.session_secret` 至少 32 个字符。
- 更新示例配置、Supervisor 示例、运行命令和管理后台地址说明。

### 2026-09-22 第十三轮

- 生成统一的接口文档 `API.md`，按当前代码实现整理公开接口和后台管理接口。
- 补充日志提交、鉴权、查询、Token、设置、状态和指标的请求响应格式。
- 在 `DESIGN.md` 和 `README.md` 中增加接口文档入口。

### 2026-09-22 第十四轮

- 生成 PHP 站点接入提示词 `PHP_INTEGRATION_PROMPT.md`。
- 提示词要求基于 ThinkPHP 5 现有日志驱动做最小改动，同步调用 `POST /api/v1/logs/batch`。
- 补充 PHP 侧配置项、日志级别映射、异常字段映射、超时、失败处理和真实集成验证要求。

### 2026-09-22 第十五轮

- 生成 OpenAPI 3.0.3 接口测试文件 `openapi.yaml`。
- 覆盖日志接入、健康检查、后台登录、日志查询、Token、设置、状态和指标接口。
- 定义项目 Bearer Token、后台会话 Cookie 和 CSRF Header 三种鉴权方案。

### 2026-09-23 第十六轮

- 将日志来源层级从“环境 > 项目 > 服务器”调整为“项目 > 节点”。
- 日志批次顶层请求字段由 `server_key` 和 `env` 合并为 `node_key`。
- `log_events` 删除 `env`、`server_key`，新增 `node_key`，并同步调整节点时间索引。
- 日志列表、详情、目录树、筛选参数、后台页面和 OpenAPI 文档统一改为按 `project_key`、`node_key` 查询和展示。
- 后台日志列表顶部不再显示项目输入框，项目由左侧目录和隐藏字段携带。
- 节点和错误场景筛选改为包含匹配，不区分大小写。

### 2026-09-23 第十七轮

- 日志列表取消上一页/下一页分页按钮。
- 列表滚动到底部后自动请求并追加下一页，分页游标仍由服务端返回。
- 列表顶部显示当前已加载条数，动态追加的行仍可点击查看详情。

### 2026-09-23 第十八轮

- 日志提交字段新增 `request_method`，用于记录 GET、POST 等 HTTP 请求方法。
- `log_events` 新增 `request_method` 字段，列表和详情接口返回该字段。
- 管理后台日志列表新增请求方法列。
- 日志详情卡片移除与列表重复的时间、级别、项目、节点、请求 IP、错误场景、请求 URL 和错误信息，只保留接收时间、错误文件和错误行号。

### 2026-09-23 第十九轮

- 管理后台日志列表列顺序调整为：时间、项目、节点、级别、方法、URL、错误场景、错误信息、请求 IP、操作。
- 请求 URL 从错误信息的兜底展示改为独立列，错误信息列只展示 `error_message`。
- 同步压缩各列固定宽度，URL 与错误信息列平分剩余空间。

### 2026-09-23 第二十轮

- 管理后台日志列表移除 URL 列，新增用户列。
- 日志提交字段新增可选 `member`，最长 255 字符。
- `log_events` 新增 `member` 字段，并建立用户与时间索引。
- 日志列表接口新增 `member` 精确查询参数。

### 2026-09-23 第二十一轮

- 日志提交字段新增可选 `session_id`，最长 255 字符。
- `log_events` 新增 `session_id` 字段，并建立 Session ID 与时间索引。
- 日志列表接口新增 `session_id` 精确查询参数。
- 管理后台日志列表和详情增加 Session ID 展示。

### 2026-09-23 第二十二轮

- 管理后台日志列表新增多选复选框、全选和批量删除。
- 新增按当前筛选条件删除全部日志。
- 左侧属性目录新增右键菜单，可按项目、节点或级别删除目录范围内的日志。
- 所有日志删除操作在发送请求前执行二次确认，用户取消时不发送删除请求。
- 项目 Token 删除保留提交前二次确认。

### 2026-09-23 第二十三轮

- 日志列表支持鼠标左键按住拖动批量勾选。
- 从起始行按下后，鼠标经过的日志行会自动按起始行的选中状态选中或取消选中。
- 拖拽期间禁用文本选择并显示单元格光标，松开鼠标后结束拖拽状态。

### 2026-09-23 第二十四轮

- 日志删除结果提示改为页面右上角浮层，不再占用日志列表布局空间。
- 浮层显示 2 秒后自动淡出，作为临时操作反馈。

### 2026-09-23 第二十五轮

- 日志记录新增持久化锁定状态，已锁定记录不能被批量删除。
- 日志列表在选择框前增加锁定图标，可直接锁定或解锁记录。
- 日志列表选中记录支持右键菜单，可删除或锁定/解锁选中日志。
- 按筛选条件删除、按目录范围删除也会跳过已锁定记录。
- 删除结果返回实际删除数和跳过的锁定记录数，并显示“跳过 N 条已锁定日志”提示。

### 2026-09-23 第二十六轮

- 日志页顶部查询、重置、左侧目录节点切换和“全部日志”改用 AJAX 异步加载。
- 筛选切换只替换日志列表、已加载数量、目录高亮和下一页游标，不再整页跳转。
- 筛选结果加载后地址栏同步当前条件，详情卡片自动切换到新列表首条日志。

### 2026-09-23 第二十七轮

- 管理后台日志列表移除“方法”和“用户”两列。
- 详情卡片中的请求方法和用户信息保持不变。

### 2026-09-23 第二十八轮

- 系统时区固定为 `Asia/Shanghai`，不再从配置读取时区。
- 后台显示、日志时间筛选和 PostgreSQL 分区边界统一使用上海时区。
- 删除 Webhook 功能，系统设置只保留非错误日志保留天数。
- 新增迁移删除历史 `alert_webhook_url` 设置。
- 调整保留天数写入顺序：提高保留天数时先补齐分区再保存设置；降低保留天数时先保存设置再清理分区，清理失败由每小时维护任务重试。
- 后台 CSP 允许模板内联样式和详情页内联关闭脚本。
- 清空日志详情时停止正在进行的请求并隐藏加载状态。
- 接收接口统一将 UUID 转为小写，保证大小写不同的事件 ID 仍按同一事件去重。
- 无限滚动失败时只替换加载文字，保留加载动画节点。

### 2026-09-23 第二十九轮

- 新增 `log_dimensions` 维度表，保存项目、节点、级别和最近出现时间。
- writer 在日志批次事务内按维度去重并更新 `log_dimensions`。
- 日志目录查询从扫描 `log_events` 改为读取 `log_dimensions`。
- 迁移时从历史日志回填维度数据。

### 2026-09-23 第三十轮

- 锁定日志可以正常勾选和参与批量选择。
- 批量删除时继续跳过锁定日志，并在确认提示中显示将被跳过的锁定记录数。

### 2026-09-23 第三十一轮

- 单击日志行只打开详情，不再改变该行复选框状态。
- 鼠标按住并移动到其他日志行时，继续按起始行的目标状态批量勾选。

### 2026-09-23 第三十二轮

- 日志筛选区域的两个批量删除按钮移动到“已加载 N 条”左侧。

### 2026-09-23 第三十三轮

- 将全部增量迁移合并为单个 `001_init.sql` 最终建表脚本。
- 新 PostgreSQL 环境首次启动时只需执行一次迁移。

### 2026-09-23 第三十四轮

- 默认监听端口从 `8080` 改为 `7878`。
- 同步更新示例配置、运行配置、接口文档和 OpenAPI 测试地址。

### 2026-09-23 第三十五轮

- 项目名称从 `log-monitor` 统一改为 `logd`。
- Go module 和内部 import 路径统一改为 `logd`。
- 示例数据库名、Supervisor 示例路径和接口文档项目名称同步更新。

### 2026-09-24 第三十六轮

- 左侧属性目录删除项目、节点或级别时，同时删除对应目录及所有下级目录。
- 目录和日志在同一个数据库事务中删除；已锁定日志仍会跳过，但对应目录项会被移除。
- 更新后台右键菜单、确认提示、接口文档和 OpenAPI 描述。

### 2026-09-24 第三十七轮

- 日志列表支持拖动表头列边界调整列宽；列总宽超过列表容器时显示底部横向滚动条。
- 可拖动列边界显示一条低对比度的竖线，悬停和拖动时高亮。
