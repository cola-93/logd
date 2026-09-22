# ThinkPHP 5 接入 logd 提示词

将下面代码块中的内容复制到 PHP 站点项目的 Codex/AI 会话中执行。

```text
你正在一个已经运行的 ThinkPHP 5 PHP 项目中工作。请把项目现有文件日志接入远程 logd 日志中心。

一、实施目标

1. 保留项目现有 ThinkPHP 日志调用方式，业务代码尽量不改调用位置。
2. 新增一个 ThinkPHP 5 自定义日志驱动 Remote，替代或并列于现有 File 驱动。
3. 日志提交必须是同步 HTTP 请求，不使用消息队列、Redis、Kafka 或异步进程。
4. 不引入 Composer 新依赖，优先使用 PHP cURL。
5. 先读取项目现有的 config/log.php、ThinkPHP 的 think\log\driver\File、异常处理和日志调用，再开始修改。
6. 采用最小改动，不重构无关代码。如果项目已有远程日志实现，优先在现有实现上修改。

二、logd 接口

接口地址：

POST {LOGD_BASE_URL}/api/v1/logs/batch
Authorization: Bearer {PROJECT_TOKEN}
Content-Type: application/json

可选请求头：

Content-Encoding: gzip

先实现普通 JSON 提交即可，不要为了 gzip 增加依赖。只有 PHP cURL 和 zlib 都可用并验证通过时，才可启用 gzip。

配置项至少包含：

- LOGD_BASE_URL，例如 http://日志中心地址:7878
- PROJECT_TOKEN，在 logd 管理后台创建的项目 Token
- NODE_KEY，当前节点唯一标识，例如 node-sh-03
- TIMEOUT，建议连接超时 1 秒、总超时 3 秒

这些配置按项目现有约定放入配置文件，不要硬编码在业务代码中，禁止把 Token 写入日志输出。

运行 PHP 的机器必须能够访问 LOGD_BASE_URL。如果 logd 只监听 127.0.0.1，需要先调整为内网可访问的监听地址或配置反向代理。

三、请求体

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
      "error_scene": "app.exception.PaymentException",
      "error_message": "支付回调验签失败",
      "error_file": "/www/shop/app/service/Payment.php",
      "error_line": 126,
      "error_stack": "..."
    }
  ]
}

四、字段规则

1. 一个请求最多 500 条日志。ThinkPHP 日志驱动一次保存超过 500 条时，按每批最多 500 条分多次同步提交。
2. project_key 不提交，logd 根据 Bearer Token 自动映射。
3. event_id 使用 UUID v4，每条日志独立生成，可基于 random_bytes 实现。
4. event_time 使用带时区和微秒的 RFC3339 时间，例如 Y-m-d\TH:i:s.uP。
5. level 只能为 INFO、WARN、ERROR、DEBUG。
6. request_ip 使用项目现有的客户端 IP 获取方式，必须传真实请求用户 IP，不能传服务器 IP。
7. member 为可选用户信息，使用当前登录用户 ID 或其他稳定的用户标识，最长 255 字符；未登录时可以不提交。
8. session_id 为可选会话标识，使用当前请求的 Session ID 或其他稳定的会话标识，最长 255 字符；没有会话时可以不提交。
9. request_method 使用当前请求的 HTTP 方法，例如 GET、POST，最长 16 字符；request_url 使用完整请求 URL；request_headers 使用请求头 JSON 对象；request_params 使用项目现有请求参数集合，尽量合并 GET、POST 和路由参数。
10. request_headers 和 request_params 必须是 JSON 对象，不是字符串、数组或标量，编码后各自不能超过 64KB。超限时要安全截断，不能让整个批次因为超限失败。
11. error_scene 必须非空，最长 255 字符。异常日志使用稳定的异常类名、业务错误码或模块动作标识；普通日志可使用 log.类型 或当前模块/控制器/方法。
12. error_message 必须非空，最长 16KB。异常使用异常消息，普通日志使用实际日志内容。
13. error_file 最长 2048 字符，error_line 必须大于 0，error_stack 最长 64KB；没有值时可以不提交或传空值。
14. 优先复用项目已有异常处理链。异常对象可用时，使用 getMessage、getFile、getLine、getTraceAsString 提取错误信息，不要重复创建两套异常处理。

五、ThinkPHP 日志级别映射

EMERGENCY、ALERT、CRITICAL、ERROR 映射为 ERROR。
WARNING、NOTICE 映射为 WARN。
INFO、SQL 映射为 INFO。
DEBUG 映射为 DEBUG。
未知级别默认映射为 INFO。

六、发送和错误处理

1. 在日志驱动 save 阶段同步调用 logd，logd 返回 202 视为成功。
2. 不自动重试，避免远程日志服务故障时拖垮业务请求。
3. 发送失败不能抛异常中断业务，不能递归写远程日志；使用 error_log 记录失败原因和 HTTP 状态码即可。
4. 收到 401、403 时记录 Token 无效或项目禁用；收到 413 时记录批次过大；收到 503 时记录 logd 队列已满。
5. 不在日志中输出 Project Token、完整 Authorization 请求头和 logd 登录凭证。
6. 不要把远程日志发送放到 register_shutdown_function 或异步任务中，本需求要求同步提交。

七、验收

1. 触发一条普通 INFO 日志和一条 PHP 异常日志。
2. 确认两次请求都返回 HTTP 202。
3. 确认 logd 后台能按 project_key、node_key、level 和时间查到日志。
4. 确认日志列表包含 request_method、member、session_id，异常日志包含 error_scene、error_message、error_file、error_line 和 error_stack。
5. 确认 logd 不可用时业务请求不会被远程日志异常中断。
6. 不要新增单元测试，优先用真实 PHP 请求做集成验证。

完成后列出修改的文件、需要补充的配置项和验证命令。若无法连接 logd，明确说明未验证，不要伪造结果。
```
