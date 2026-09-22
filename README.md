# logd

集中式 PHP 日志记录系统。当前实现包含 `logd` 服务、PostgreSQL 迁移、分区初始化、日志接收、有界内存队列、批量写库，以及查询 API 和 Web 管理后台。

完整接口说明见 [`API.md`](API.md)，可导入 API 调试软件的 OpenAPI 文件见 [`openapi.yaml`](openapi.yaml)。

PHP 站点接入提示词见 [`PHP_INTEGRATION_PROMPT.md`](PHP_INTEGRATION_PROMPT.md)。

## 构建

```bash
go test ./...
go build -o logd ./cmd/logd
```

## 配置

复制 `config.example.yml`，填写实际 PostgreSQL 连接信息和后台凭证。开发和部署环境的真实配置使用独立文件，不提交到版本库。

数据库账号需要拥有目标数据库，因为 `logd` 会执行迁移并创建、删除日志分区。

## 运行

```bash
./logd -config config.yml
```

启动时会依次执行数据库迁移、读取系统设置、创建接收窗口所需的日志分区，然后开始监听 HTTP 端口。

队列、写入批次、重试和退出排空参数在 `queue` 配置段中设置。

后台访问密码和会话签名密钥在配置文件的 `admin` 配置段中设置：

```yaml
admin:
  password: "replace-me"
  session_secret: "replace-with-at-least-32-random-characters"
```

`password` 在程序启动时转换为 bcrypt 哈希，`session_secret` 至少 32 个字符，用于签名后台会话 Cookie。配置文件中包含后台凭证，生产环境需要限制配置文件读取权限。

健康检查：

```text
GET /healthz
GET /readyz
GET /metrics
```

日志接收：

```http
POST /api/v1/logs/batch
Authorization: Bearer <token>
Content-Type: application/json
Content-Encoding: gzip
```

请求体格式见 [`API.md`](API.md)。接收成功返回 `202`，表示日志已经进入内存队列，不表示已经提交 PostgreSQL。

接口字段、管理 API、错误码和调用示例见 [`API.md`](API.md)。

后台入口：

```text
GET /admin/login
```

默认监听 `127.0.0.1:7878` 时，本机访问地址为 `http://127.0.0.1:7878/admin/login`。

## 当前进度

已完成：

- Go 项目骨架和 YAML 配置。
- PostgreSQL 连接池和版本迁移。
- `log_events`、`project_tokens`、`system_settings` 等基础表。
- `ERROR` 月分区和非错误日分区初始化及清理。
- `/healthz` 和 `/readyz`。
- 项目 Token 的 SHA-256 哈希校验和启用状态检查。
- gzip 解压、5MB 解压后限制和批次字段完整校验。
- 有界内存队列和整批原子入队。
- 后台 writer 按批次写入 PostgreSQL，使用事务和 `ON CONFLICT DO NOTHING`。
- 后台日志目录从 `log_dimensions` 读取，writer 在日志事务内同步维护项目、节点和级别维度。
- 写入失败指数退避重试和正常退出队列排空。
- 后台访问密码登录、签名 HttpOnly Cookie 会话和 CSRF 校验。
- 日志列表、详情、项目 Token、系统设置和系统状态页面。
- 日志查询和详情 JSON API。
- 项目 Token 列表、新增、修改、删除 API，以及创建后立即刷新内存缓存。
- 保留天数修改后同步调整分区和接收校验窗口。
- 每小时运行一次分区维护。
- Prometheus 文本格式 `/metrics` 指标，不依赖 Prometheus 客户端库。
- PostgreSQL、队列、写入停滞和接收错误率告警。
- 状态页面和状态 API 展示当前告警。
- 完整 API 接口文档 `API.md`，覆盖日志接入、查询、后台管理和运维接口。
- 提供 OpenAPI 3.0.3 接口测试文件 `openapi.yaml`。
- ThinkPHP 5 站点接入提示词 `PHP_INTEGRATION_PROMPT.md`。
