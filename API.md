# logd API 接口文档

> 本文档以当前 Go 代码实现为准。最后更新：2026-09-23。
>
> 可导入 API 调试软件的 OpenAPI 3.0.3 文件：[`openapi.yaml`](openapi.yaml)。

## 1. 通用约定

### 1.1 服务地址

默认监听地址：

```text
http://127.0.0.1:7878
```

实际部署时以 `config.yml` 中的 `server.listen` 为准。

### 1.2 鉴权方式

日志提交接口使用项目 Token：

```http
Authorization: Bearer <project-token>
```

后台 JSON API 使用后台会话 Cookie：

```text
logd_admin=<signed-session-cookie>
```

登录成功后，修改类后台 API 还必须携带登录响应返回的 CSRF Token：

```http
X-CSRF-Token: <csrf-token>
```

CSRF 校验适用于除 `GET` 以外的后台 API，包括 `POST`、`PATCH` 和 `DELETE`。

### 1.3 时间格式

日志接口推荐使用带时区的 RFC3339 时间：

```text
2026-09-21T12:30:00.123+08:00
```

后台查询接口还接受以下形式，此时固定按 `Asia/Shanghai` 解释：

```text
2026-09-21T12:30:00
2026-09-21T12:30
```

### 1.4 JSON 错误格式

公开日志接口错误：

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

后台管理 API 错误：

```json
{
  "error": {
    "code": "INVALID_QUERY",
    "message": "start_time 和 end_time 不能为空"
  }
}
```

`index` 和 `field` 只在能定位到具体批次元素时返回。

### 1.5 常用状态码

| 状态码 | 说明 |
|---:|---|
| `200` | 请求成功 |
| `201` | 资源创建成功 |
| `202` | 日志批次已进入内存队列 |
| `204` | 请求成功且无响应体 |
| `400` | 请求参数、JSON 或字段校验失败 |
| `401` | 项目 Token 或后台会话无效 |
| `403` | 项目被禁用、CSRF 无效 |
| `404` | 资源不存在 |
| `413` | 解压后的日志请求体超过 5MB |
| `500` | 服务内部错误 |
| `503` | 日志队列已满或服务尚未就绪 |

## 2. PHP 日志提交接口

### 2.1 提交日志批次

```http
POST /api/v1/logs/batch
Authorization: Bearer <project-token>
Content-Type: application/json
Content-Encoding: gzip
```

`Content-Encoding` 可省略。省略时请求体使用普通 JSON；设置为 `gzip` 时先对 JSON 请求体进行 gzip 压缩。

请求体：

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
      "request_headers": {
        "content-type": "application/json",
        "user-agent": "PHP/8.3"
      },
      "request_params": {
        "order_id": 10001
      },
      "error_scene": "payment.callback.signature_invalid",
      "error_message": "支付回调验签失败",
      "error_file": "/www/shop/app/service/Payment.php",
      "error_line": 126,
      "error_stack": "..."
    }
  ]
}
```

字段说明：

| 字段 | 必填 | 类型 | 限制 |
|---|---:|---|---|
| `node_key` | 是 | string | 1 到 64 个字符 |
| `logs` | 是 | array | 1 到 500 条 |
| `logs[].event_id` | 是 | string | UUID，批次内不能重复 |
| `logs[].event_time` | 是 | string | RFC3339 时间，必须在接收窗口内 |
| `logs[].level` | 是 | string | `INFO`、`WARN`、`ERROR`、`DEBUG` |
| `logs[].request_ip` | 是 | string | 合法 IPv4 或 IPv6 |
| `logs[].member` | 否 | string | 最大 255 个字符，用于精确查询 |
| `logs[].session_id` | 否 | string | 最大 255 个字符，用于精确查询 |
| `logs[].request_method` | 是 | string | 1 到 16 个字符，保存时转为大写 |
| `logs[].request_url` | 是 | string | 1 到 8192 个字符 |
| `logs[].request_headers` | 否 | object | JSON 对象，最大 64KB |
| `logs[].request_params` | 否 | object | JSON 对象，最大 64KB |
| `logs[].error_scene` | 是 | string | 1 到 255 个字符 |
| `logs[].error_message` | 是 | string | 1 到 16KB |
| `logs[].error_file` | 否 | string | 最大 2048 个字符 |
| `logs[].error_line` | 否 | integer | 必须大于 0 |
| `logs[].error_stack` | 否 | string | 最大 64KB |

接收规则：

- `project_key` 不提交，由 Bearer Token 映射得到。
- `node_key` 是批次顶层字段，表示日志来源节点。
- 整个批次完整校验，任意一条不合法时整个请求失败，不支持部分成功。
- 解压后的请求体最大为 5MB。
- `event_time` 不能晚于服务当前时间 5 分钟。
- `ERROR` 的 `event_time` 不能早于服务当前时间 31 天。
- 其他级别的 `event_time` 不能早于服务当前时间 `retention_days` 天。
- 成功入队后返回 `202`。该状态不表示 PostgreSQL 已经提交。

成功响应：

```json
{
  "accepted": 1
}
```

错误响应：

```json
{
  "error": {
    "code": "INVALID_LOG",
    "message": "event_id is duplicated in batch",
    "index": 1,
    "field": "event_id"
  }
}
```

可能返回的接口错误码：

| 状态码 | `code` | 说明 |
|---:|---|---|
| `400` | `INVALID_ENCODING` | 不支持的 `Content-Encoding` |
| `400` | `INVALID_BODY` | gzip 或请求体读取失败 |
| `400` | `INVALID_JSON` | JSON 格式错误或包含多个 JSON 值 |
| `400` | `INVALID_LOG` | 批次字段校验失败 |
| `401` | `UNAUTHORIZED` | Bearer Token 缺失或无效 |
| `403` | `PROJECT_DISABLED` | Token 对应项目已禁用 |
| `413` | `BODY_TOO_LARGE` | 解压后的请求体超过 5MB |
| `503` | `QUEUE_FULL` | 内存接收队列已满 |

调用示例：

```bash
curl -X POST http://127.0.0.1:7878/api/v1/logs/batch \
  -H "Authorization: Bearer <project-token>" \
  -H "Content-Type: application/json" \
  -d '{
    "node_key": "node-sh-03",
    "logs": [{
      "event_id": "4f5b4fd5-42b8-47d5-a0db-0273cd76f574",
      "event_time": "2026-09-21T12:30:00+08:00",
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
    }]
  }'
```

## 3. 运维接口

### 3.1 存活检查

```http
GET /healthz
```

无需鉴权。

响应：

```json
{
  "status": "ok"
}
```

### 3.2 就绪检查

```http
GET /readyz
```

无需鉴权。服务会检查分区是否就绪以及 PostgreSQL 是否可连接。

成功响应：

```json
{
  "status": "ready"
}
```

未就绪响应：

```http
HTTP/1.1 503 Service Unavailable
```

```json
{
  "status": "not_ready"
}
```

### 3.3 Prometheus 指标

```http
GET /metrics
```

无需鉴权，响应类型为：

```text
Content-Type: text/plain; version=0.0.4; charset=utf-8
```

当前指标：

| 指标 | 类型 | 说明 |
|---|---|---|
| `logd_ingest_requests_total{status}` | counter | 接收接口请求数 |
| `logd_ingest_events_total{level}` | counter | 成功入队的事件数 |
| `logd_queue_depth` | gauge | 当前队列深度 |
| `logd_queue_capacity` | gauge | 队列容量 |
| `logd_queue_rejected_total` | counter | 队列满导致的批次拒绝数 |
| `logd_db_write_batches_total{result}` | counter | PostgreSQL 写入批次数 |
| `logd_db_write_events_total{result}` | counter | PostgreSQL 写入事件数 |
| `logd_db_write_duration_seconds` | histogram | PostgreSQL 写入耗时 |
| `logd_last_successful_db_write_timestamp_seconds` | gauge | 最近一次成功写入的 Unix 时间戳 |

## 4. 后台登录接口

### 4.1 登录

```http
POST /api/v1/admin/login
Content-Type: application/json
```

请求体：

```json
{
  "password": "admin123"
}
```

成功响应：

```json
{
  "csrf_token": "base64url-token"
}
```

成功后服务端设置 `logd_admin` HttpOnly Cookie，有效期 12 小时。HTTPS 请求下 Cookie 自动带 `Secure`。

错误：

| 状态码 | `code` | 说明 |
|---:|---|---|
| `400` | `INVALID_JSON` | 请求体不是有效 JSON |
| `401` | `INVALID_PASSWORD` | 访问密码错误 |
| `500` | `INTERNAL_ERROR` | 创建会话失败 |

### 4.2 退出

```http
POST /api/v1/admin/logout
X-CSRF-Token: <csrf-token>
```

需要有效后台会话和 CSRF Token。

成功响应：

```http
HTTP/1.1 204 No Content
```

## 5. 日志查询接口

日志查询接口需要有效后台会话，不复用 PHP 日志接口的项目 Token。

### 5.1 日志列表

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
| `level` | 否 | `INFO`、`WARN`、`ERROR`、`DEBUG` |
| `error_scene` | 否 | 包含匹配（不区分大小写） |
| `request_ip` | 否 | 精确匹配 |
| `member` | 否 | 精确匹配 |
| `session_id` | 否 | 精确匹配 |
| `limit` | 否 | 默认 50，范围 1 到 200 |
| `cursor` | 否 | 上一页响应中的 `next_cursor` |

规则：

- `start_time` 必须早于 `end_time`。
- 单次查询时间跨度不能超过 31 天。
- 结果按 `event_time DESC, event_id DESC` 排序。
- 列表不返回 `request_headers`、`request_params`、`error_file`、`error_line` 和 `error_stack`。
- 当前不支持 `error_message` 和 `error_stack` 模糊搜索。

成功响应：

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
      "locked": false,
      "request_ip": "1.2.3.4",
      "member": "10001",
      "session_id": "2f3d9f38-1b3d-4e5c-9e74-9dc49b4f2fd0",
      "request_method": "POST",
      "request_url": "https://api.example.com/order/create",
      "error_scene": "payment.callback.signature_invalid",
      "error_message": "支付回调验签失败"
    }
  ],
  "next_cursor": "b3BhcXVlLWN1cnNvcg",
  "has_more": true
}
```

没有下一页时，`next_cursor` 返回空字符串，`has_more` 返回 `false`。

示例：

```bash
curl -G http://127.0.0.1:7878/api/v1/logs \
  -H "Cookie: logd_admin=<session-cookie>" \
  --data-urlencode "start_time=2026-09-21T00:00:00+08:00" \
  --data-urlencode "end_time=2026-09-22T00:00:00+08:00" \
  --data-urlencode "level=ERROR" \
  --data-urlencode "node_key=node-sh-03" \
  --data-urlencode "member=10001" \
  --data-urlencode "session_id=2f3d9f38-1b3d-4e5c-9e74-9dc49b4f2fd0" \
  --data-urlencode "limit=50"
```

### 5.2 日志详情

```http
GET /api/v1/logs/{event_id}?level=ERROR&event_time=2026-09-21T12%3A30%3A00.123%2B08%3A00
```

`event_id`、`level` 和 `event_time` 必须同时提交。`level` 和 `event_time` 是分区键，用于避免扫描所有历史分区。

成功响应：

```json
{
  "event_id": "4f5b4fd5-42b8-47d5-a0db-0273cd76f574",
  "event_time": "2026-09-21T12:30:00.123+08:00",
  "received_at": "2026-09-21T12:30:00.456+08:00",
  "project_key": "shop-api",
  "node_key": "node-sh-03",
  "level": "ERROR",
  "locked": false,
  "request_ip": "1.2.3.4",
  "member": "10001",
  "session_id": "2f3d9f38-1b3d-4e5c-9e74-9dc49b4f2fd0",
  "request_method": "POST",
  "request_url": "https://api.example.com/order/create",
  "error_scene": "payment.callback.signature_invalid",
  "error_message": "支付回调验签失败",
  "request_headers": {
    "content-type": "application/json"
  },
  "request_params": {
    "order_id": 10001
  },
  "error_file": "/www/shop/app/service/Payment.php",
  "error_line": 126,
  "error_stack": "..."
}
```

未提交 `member` 或 `session_id` 时列表和详情接口返回空字符串；其他可空字段在未提交时返回 `null`。

错误：

| 状态码 | `code` | 说明 |
|---:|---|---|
| `400` | `INVALID_QUERY` | `event_id`、`level` 或 `event_time` 无效 |
| `401` | `UNAUTHORIZED` | 后台会话无效 |
| `404` | `NOT_FOUND` | 日志不存在 |
| `500` | `INTERNAL_ERROR` | 查询失败 |

## 6. 项目 Token 管理接口

所有接口需要有效后台会话。`POST`、`PATCH`、`DELETE` 还需要 CSRF Token。

### 6.1 获取 Token 列表

```http
GET /api/v1/admin/project-tokens
```

响应：

```json
{
  "items": [
    {
      "id": "95aa72c4-9649-4d28-8e5f-982ffbdf7e45",
      "project_key": "shop-api",
      "name": "商城 API",
      "enabled": true,
      "created_at": "2026-09-21T17:30:00+08:00",
      "updated_at": "2026-09-21T17:30:00+08:00"
    }
  ]
}
```

响应不会返回 Token 明文和 Token 哈希。

### 6.2 创建 Token

```http
POST /api/v1/admin/project-tokens
Content-Type: application/json
X-CSRF-Token: <csrf-token>
```

请求体：

```json
{
  "project_key": "shop-api",
  "name": "商城 API",
  "enabled": true
}
```

`enabled` 可省略，默认 `true`。`project_key` 和 `name` 长度分别为 1 到 64、1 到 100 个字符。

成功响应：

```http
HTTP/1.1 201 Created
```

```json
{
  "token": "base64url-project-token",
  "project_token": {
    "id": "95aa72c4-9649-4d28-8e5f-982ffbdf7e45",
    "project_key": "shop-api",
    "name": "商城 API",
    "enabled": true,
    "created_at": "2026-09-21T17:30:00+08:00",
    "updated_at": "2026-09-21T17:30:00+08:00"
  }
}
```

Token 明文只在创建响应中返回一次，数据库仅保存 SHA-256 哈希。

### 6.3 修改 Token

```http
PATCH /api/v1/admin/project-tokens/{id}
Content-Type: application/json
X-CSRF-Token: <csrf-token>
```

请求体中的字段均可选，只提交需要修改的字段：

```json
{
  "project_key": "shop-api-v2",
  "name": "商城 API V2",
  "enabled": false
}
```

成功响应为更新后的 Token 记录：

```json
{
  "id": "95aa72c4-9649-4d28-8e5f-982ffbdf7e45",
  "project_key": "shop-api-v2",
  "name": "商城 API V2",
  "enabled": false,
  "created_at": "2026-09-21T17:30:00+08:00",
  "updated_at": "2026-09-21T17:31:00+08:00"
}
```

### 6.4 删除 Token

```http
DELETE /api/v1/admin/project-tokens/{id}
X-CSRF-Token: <csrf-token>
```

成功响应：

```http
HTTP/1.1 204 No Content
```

删除是物理删除。Token 增删改后，`logd` 会立即刷新内存缓存。

### 6.5 Token 管理错误码

| 状态码 | `code` | 说明 |
|---:|---|---|
| `400` | `INVALID_JSON` | 请求体不是有效 JSON |
| `400` | `INVALID_TOKEN_ID` | Token ID 不是 UUID |
| `400` | `INVALID_TOKEN` | 字段校验失败或数据库约束失败 |
| `401` | `UNAUTHORIZED` | 后台会话无效 |
| `403` | `INVALID_CSRF` | CSRF Token 无效 |
| `404` | `NOT_FOUND` | Token 不存在 |
| `500` | `INTERNAL_ERROR` | 读取、修改或刷新缓存失败 |

## 7. 系统设置接口

所有接口需要有效后台会话。修改接口还需要 CSRF Token。

### 7.1 获取设置

```http
GET /api/v1/admin/settings
```

响应：

```json
{
  "retention_days": 30
}
```

### 7.2 修改设置

```http
PATCH /api/v1/admin/settings
Content-Type: application/json
X-CSRF-Token: <csrf-token>
```

请求体：

```json
{
  "retention_days": 30
}
```

字段规则：

| 字段 | 类型 | 说明 |
|---|---|---|
| `retention_days` | integer | 可选，范围 1 到 3650 |

提高保留天数时，服务会先补齐新保留窗口内的分区，再保存设置；降低保留天数时，先保存设置，再清理过期分区。设置保存成功后会同步更新日志接收校验窗口。

成功响应为更新后的设置：

```json
{
  "retention_days": 30
}
```

## 8. 系统状态接口

```http
GET /api/v1/admin/status
```

需要有效后台会话。

响应：

```json
{
  "database_ok": true,
  "queue_depth": 12,
  "queue_capacity": 50000,
  "retention_days": 30,
  "last_write": "2026-09-22T11:30:00.123+08:00",
  "alerts": [
    {
      "key": "queue_high",
      "message": "接收队列使用率超过 80%",
      "since": "2026-09-22T11:20:00+08:00"
    }
  ]
}
```

`last_write` 尚无成功写入时为 `null`。`alerts` 当前没有告警时为空数组。

当前告警键：

| `key` | 条件 |
|---|---|
| `database_unavailable` | PostgreSQL 连续不可用 2 分钟 |
| `queue_high` | 队列使用率超过 80%，持续 5 分钟 |
| `write_stalled` | 队列非空且连续 2 分钟没有成功写入 |
| `ingest_error_rate` | 最近 5 分钟接收错误率超过 5% |

## 9. Web 管理后台页面

页面接口使用浏览器会话和表单 CSRF Token，不是 JSON API。

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/admin/login` | 登录页 |
| `POST` | `/admin/login` | 登录表单提交 |
| `POST` | `/admin/logout` | 退出登录 |
| `GET` | `/admin/logs` | 日志列表 |
| `POST` | `/admin/logs/delete-selected` | 批量删除已选日志 |
| `POST` | `/admin/logs/delete-filter` | 删除当前筛选条件下的全部日志 |
| `POST` | `/admin/logs/delete-scope` | 删除项目、节点或级别范围内的目录及日志 |
| `POST` | `/admin/logs/lock-selected` | 锁定或解锁已选日志 |
| `GET` | `/admin/logs/{event_id}` | 日志详情 |
| `GET` | `/admin/project-tokens` | 项目 Token 列表 |
| `POST` | `/admin/project-tokens` | 创建 Token |
| `POST` | `/admin/project-tokens/{id}` | 修改 Token |
| `POST` | `/admin/project-tokens/{id}/delete` | 删除 Token |
| `GET` | `/admin/settings` | 系统设置 |
| `POST` | `/admin/settings` | 修改系统设置 |
| `GET` | `/admin/status` | 系统状态 |
| `GET` | `/assets/*` | 后台静态资源 |

三个日志删除接口都要求有效后台会话和 `X-CSRF-Token`，成功响应格式为：

```json
{
  "deleted": 12,
  "skipped_locked": 2
}
```

删除前页面会弹出二次确认，用户取消时不会发送请求。已锁定的日志不会删除，必须先解锁；`skipped_locked` 返回因锁定而跳过的记录数。

#### 9.1 批量删除已选日志

```http
POST /admin/logs/delete-selected
Content-Type: application/json
X-CSRF-Token: <csrf-token>
```

```json
{
  "items": [
    {
      "event_id": "4f5b4fd5-42b8-47d5-a0db-0273cd76f574",
      "level": "ERROR",
      "event_time": "2026-09-21T12:30:00.123+08:00"
    }
  ]
}
```

只删除当前已选中的日志记录。

#### 9.2 按条件删除日志

```http
POST /admin/logs/delete-filter
Content-Type: application/x-www-form-urlencoded
X-CSRF-Token: <csrf-token>
```

表单字段与 `GET /admin/logs` 的查询参数一致，可传 `start_time`、`end_time`、`project_key`、`node_key`、`level`、`error_scene`、`member`、`session_id` 和 `request_ip`。删除范围不受当前已加载条数限制。

#### 9.3 按目录范围删除目录及日志

```http
POST /admin/logs/delete-scope
Content-Type: application/x-www-form-urlencoded
X-CSRF-Token: <csrf-token>
```

```text
project_key=shop-api&node_key=node-sh-03&level=ERROR
```

`project_key` 必填；`node_key` 和 `level` 为空时分别表示不限制该层级。

该接口在同一个事务中删除匹配的日志和属性目录项。删除项目会同时删除其下所有节点和级别目录，删除节点会同时删除其下所有级别目录。已锁定日志会被跳过，但对应的目录项仍会被删除。

#### 9.4 锁定或解锁已选日志

```http
POST /admin/logs/lock-selected
Content-Type: application/json
X-CSRF-Token: <csrf-token>
```

```json
{
  "items": [
    {
      "event_id": "4f5b4fd5-42b8-47d5-a0db-0273cd76f574",
      "level": "ERROR",
      "event_time": "2026-09-21T12:30:00.123+08:00"
    }
  ],
  "locked": true
}
```

`locked` 为 `true` 时锁定记录，为 `false` 时解锁记录。锁定后的记录不能参与批量删除、按条件删除或目录范围删除。

成功响应：

```json
{
  "updated": 1
}
```
