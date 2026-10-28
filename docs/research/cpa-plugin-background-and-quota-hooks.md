# CPA 插件后台运行与 Codex 额度 hook 调研

核验日期：2026-10-28。

核验范围：当前插件 `7aee5a1df1a360e4db33a05df0ccd19f104c2770`；其 `go.mod` 指定的官方 CLIProxyAPI `v8.0.36`，对应提交 `20c9095cac089c6a8e40554debe4dd8a19d4c14f`。核验了本地官方 Go module 源码，并成功读取官方 GitHub 对应提交中的 Codex 执行器、usage reporter、插件适配器文件。未把邻近目录的旧版 CPA 当作此版本依据；未声称覆盖之后所有版本。

## 结论

| 问题 | 结论 |
|---|---|
| 进程内动态库插件能否不持有管理密钥、后台运行？ | **能；当前插件已经如此运行。** 启动后台 worker、获取账户凭据、执行定时任务，都不依赖浏览器或 Management HTTP。 |
| CPA 配置了管理密钥，插件还能否后台运行？ | **能。** 管理密钥保护管理 HTTP 接口，不会给进程内 worker 增加登录要求；插件也不必保存该密钥。 |
| 独立脚本/外部服务通过 Management API 管理 CPA，能否无管理密钥？ | **不能按此接口方式免密管理。** 它与进程内 host ABI 是不同访问方式，不能混为一谈。 |
| 有没有类似 hook 的机制？ | **有。** 包括请求拦截、请求完成、响应/流式拦截、WebSocket 响应事件、用量回调等。 |
| 有没有专用的 `onCodexQuotaExhausted`/账户额度耗尽事件？ | **在核验版本的公开动态插件 ABI 中未找到。** 但并不意味着只能轮询或必须修改上游。 |
| 能否利用现有 hook，在 Codex 账户返回额度耗尽错误时触发操作？ | **可以，优先使用 `usage.handle`。** 已核对的 Codex HTTP 执行路径会单独发布失败尝试，记录含账户标识、失败状态码与错误内容；插件可据此唤醒后台核验。 |
| 当前 auto-reset 插件是否已经订阅这种失败回调？ | **没有。** 当前只声明 `ManagementAPI` 能力，自动行为按重置额度即将过期的时间驱动。 |

## 1. 为什么后台运行不需要管理密钥

当前插件的启动链路是：

```text
CPA 加载动态库
  → plugin.register / plugin.reconfigure
  → configurePlugin(config_yaml / 持久化 state)
  → NewWorker(...)
  → go globalWorker.Run()
```

`configurePlugin` 直接启动后台 goroutine，没有要求输入管理密钥，也没有向 CPA Management HTTP 发起登录。账户凭据由 `hostCredsLoader` 调用 `host.auth.list`、`host.auth.get` 获取。重置验证成功后，通过 `host.routing.reset_cooldown` 清除核心冷却，而不是通过管理 HTTP 回环调用。[P1][P2][P3][U1][U2]

因此，不打开浏览器、关闭插件页面、页面里没有输入管理密钥，都不会阻止已配置的后台任务运行。前提是 CPA 进程持续运行、插件加载成功、目标账户已启用，并且账户凭据仍然有效。

### 三类凭据必须分开

- **CPA 管理密钥**：用于访问 `/v0/management/...` 等管理 HTTP 接口。
- **客户端调用 CPA 的 API key**：供客户端调用模型代理接口，不能替代管理密钥。
- **Codex OAuth 账户凭据**：用于插件向 ChatGPT/OpenAI 后端请求额度与重置接口；后台仍需要这类上游凭据。[P2][P5][U1][U3]

“无需管理密钥”不等于“无需凭据”。动态库插件能获得账户原始 JSON，因此它处在高信任边界内，不是只拥有匿名访问权限。[U1]

### 无管理界面的配置条件

当前插件默认 `enabled_accounts` 为空，不会自动选择所有账户。可预先通过宿主传入的 YAML 配置启用账户，或者先用管理页面配置一次，让配置持久化，后续关闭页面仍运行。[P1][P4]

当前版本存在配置优先级：已有持久化配置时优先使用 `state.json` 中配置；只有 `state_dir` 的 YAML 覆盖被单独保留。不能保证修改 YAML 中的账户列表就一定覆盖已有 state。[P1]

### Management HTTP 的权限边界

- 进程内后台 worker 不需要 Management HTTP，因此也不需要为它打开远程管理。
- 浏览器或外部程序调用插件管理接口，仍须提供有效管理密钥。
- 若没有配置、环境变量或本地 TUI 密码中的任何管理凭据，管理路由不启用，管理请求返回 **404**，不是匿名管理。
- 非回环客户端访问管理接口还受 `allow-remote` 策略约束，以 CPA 识别到的客户端 IP 为准；环境变量配置可能提供远程策略覆盖。
- `/v0/resource/plugins/...` 资源页与受保护的 Management API 是不同入口。能够获取 HTML，不代表能够匿名获取状态、改配置或执行 Reset。[P5][U3][U4]

## 2. 有哪些现成 hook

| 机制 | 动态插件 ABI 方法 | 用于本需求的判断 |
|---|---|---|
| 凭据选择前/后请求拦截 | `request.intercept_before` / `request.intercept_after` | 均在执行请求前，不是额度耗尽事件。 |
| 请求完成通知 | `request.complete` | 可看请求终态；不能单凭最终成功/失败还原所有失败账户尝试，载荷没有专门的账户字段。 |
| 成功非流式响应拦截 | `response.intercept_after` | 接口定义是成功响应拦截，不适合作为 429 失败通知。 |
| 流式块拦截 | `response.intercept_stream_chunk` | 可以处理数据块，但不是完整的凭据失败事件订阅。 |
| WebSocket 响应观察 | `websocket.response_event` | 可观察相关响应事件，有账户上下文；不是统一覆盖 HTTP/WS 的额度耗尽事件。 |
| **执行用量/失败回调** | **`usage.handle`** | **最贴近需求。** `UsageRecord` 包含 `Provider`、`AuthID`、`AuthIndex`、`Failed`、`Failure.StatusCode`、`Failure.Body`、`ResponseHeaders`。 |

方法常量见 [U5]，接口与载荷见 [U6]。`QuotaProvider` 则是插件提供额度查询/重置能力的接口，不等同于订阅核心的耗尽事件。

另有 Go SDK 的 `coreauth.Hook.OnResult(ctx, result)`，属于核心 Manager 的 Go 接口，不是动态库插件可直接声明使用的 C ABI 方法。动态插件不必为了本需求去接这个内部接口，因为已经有 `usage.handle`。[U7]

## 3. 为什么自动切号不会隐藏所有失败事件

不能只盯着最终客户端响应。核验版本的 Codex 主执行路径是：

1. `CodexExecutor.Execute` 为本次账户执行创建 `UsageReporter`，并 `defer reporter.TrackFailure(ctx, &err)`。[U8]
2. HTTP 非 2xx，包括 429，会读取错误体并返回对应错误。[U8]
3. `TrackFailure` 发布失败记录；记录明确保存本次执行的 `AuthID`、`AuthIndex`、`Failed` 与错误状态码/内容。[U9]
4. `usage.PublishRecord` 把记录加入异步队列。插件适配器将失败字段原样映射到 `pluginapi.UsageRecord`，再通过 `usage.handle` 调用动态插件。[U10][U11]

因此，对于已核验的路径，即使账户 A 的 429 后核心切换到账户 B 并最终返回成功，A 的失败尝试仍会进入独立的 usage 上报链路。**不能用“最终请求成功了”推导“插件看不到 A 的失败”。**

Codex HTTP 流式入口也创建 reporter 并跟踪失败；若干流内错误分支还显式调用 `PublishFailure`。但这不等于所有执行器、所有流式/WS 分支、所有前置拒绝都具有完全相同的事件语义。[U12]

### 能拿到什么，不能保证什么

- 可以根据回调定位失败账户，并检查状态码与错误内容。
- `UsageRecord` 没有专门的 `RetryAfter` 字段；可用错误体中已有的 `resets_at` / `resets_in_seconds` 等信息，必要时再查宿主运行状态或上游 usage。不能保证每个错误都带恢复时间。[U6][U13]
- 这是异步执行观测，不是“失败请求暂停，等待插件重置后继续”的同步拦截点。不能保证挽救正在失败的那一次请求。[U10]
- 未执行请求的账户不会凭空产生失败回调；在其他客户端耗尽、或用量刚好降到零但尚未再失败的账户，仍需主动查询。
- 单个 429 不能证明周额度耗尽；即便 `usage_limit_reached` 表示使用额度受限，也不应直接认定耗尽的是可用 Reset Bank 恢复的周窗口。CPA 源码明确区分该错误与普通 `rate_limit_error` / `rate_limit_exceeded`。[U13]
- 回调中可能包含调用方 API key、上游错误内容等敏感信息；不要整条记录直接写日志或发送第三方。[U6]

## 4. 对当前插件的最小建议

**后台保留进程内 worker，不引入管理密钥依赖。若新增“额度错误触发核验”，先接现有 `usage.handle`，无须以修改 CPA 上游为前提。**

最小行为链路可以是：

```text
usage.handle 收到 Codex 失败记录
  → 限定已启用账户
  → 识别额度类错误，不把普通 429 当周额度耗尽
  → 合并重复通知，唤醒后台 worker 重新查询 usage / reset credits
  → 按明确授权的策略决定是否消耗重置额度
  → 成功验证后沿用 host.routing.reset_cooldown
```

当前插件 `pluginRegistration` 只声明 `ManagementAPI`；`handleMethod` 也未处理 `usage.handle`，因此上述接入需要修改本插件，而不是已经自动存在的能力。[P6]

现有自动策略是“重置额度即将过期才使用”，不是“账户额度耗尽立即使用”。事件触发应先作为核验信号，不能未经决策便改变消耗额度的政策。若以后要求完整、结构化的每凭据配额状态变更、精确重试时间和同步处置能力，再评估新增上游 ABI；不是本需求的第一步。

## 证据索引

### 当前插件，提交 `7aee5a1df1a360e4db33a05df0ccd19f104c2770`

- [P1] [`configurePlugin`：配置优先级与后台启动](https://github.com/xz-dev/cpa-plugin-codex-auto-reset/blob/7aee5a1df1a360e4db33a05df0ccd19f104c2770/main.go#L265-L369)
- [P2] [`hostCredsLoader`、host auth 回调调用](https://github.com/xz-dev/cpa-plugin-codex-auto-reset/blob/7aee5a1df1a360e4db33a05df0ccd19f104c2770/main.go#L597-L646)
- [P3] [`afterVerifiedReset`、冷却重置](https://github.com/xz-dev/cpa-plugin-codex-auto-reset/blob/7aee5a1df1a360e4db33a05df0ccd19f104c2770/main.go#L439-L468)
- [P4] [`DefaultConfig` 默认不启用账户](https://github.com/xz-dev/cpa-plugin-codex-auto-reset/blob/7aee5a1df1a360e4db33a05df0ccd19f104c2770/config.go#L111-L140)
- [P5] [README：权限边界、实际使用的 wham 端点与工作策略](https://github.com/xz-dev/cpa-plugin-codex-auto-reset/blob/7aee5a1df1a360e4db33a05df0ccd19f104c2770/README.md)
- [P6] [`pluginRegistration`](https://github.com/xz-dev/cpa-plugin-codex-auto-reset/blob/7aee5a1df1a360e4db33a05df0ccd19f104c2770/main.go#L515-L526)；[`handleMethod`](https://github.com/xz-dev/cpa-plugin-codex-auto-reset/blob/7aee5a1df1a360e4db33a05df0ccd19f104c2770/main.go#L249-L263)

### CLIProxyAPI `v8.0.36`，提交 `20c9095cac089c6a8e40554debe4dd8a19d4c14f`

- [U1] [host auth callbacks](https://github.com/router-for-me/CLIProxyAPI/blob/20c9095cac089c6a8e40554debe4dd8a19d4c14f/internal/pluginhost/auth_callbacks.go#L61-L95)
- [U2] [host routing reset cooldown](https://github.com/router-for-me/CLIProxyAPI/blob/20c9095cac089c6a8e40554debe4dd8a19d4c14f/internal/pluginhost/routing_callbacks.go#L11-L46)
- [U3] [管理路由启用条件](https://github.com/router-for-me/CLIProxyAPI/blob/20c9095cac089c6a8e40554debe4dd8a19d4c14f/internal/api/server.go#L248-L257)；[远程管理与密钥校验](https://github.com/router-for-me/CLIProxyAPI/blob/20c9095cac089c6a8e40554debe4dd8a19d4c14f/internal/api/handlers/management/handler.go#L326-L403)
- [U4] [插件 Management fallback 与 resource 路由](https://github.com/router-for-me/CLIProxyAPI/blob/20c9095cac089c6a8e40554debe4dd8a19d4c14f/internal/api/server_management.go#L217-L310)
- [U5] [动态插件 ABI 方法常量](https://github.com/router-for-me/CLIProxyAPI/blob/20c9095cac089c6a8e40554debe4dd8a19d4c14f/sdk/pluginabi/types.go#L65-L82)
- [U6] [请求扩展接口](https://github.com/router-for-me/CLIProxyAPI/blob/20c9095cac089c6a8e40554debe4dd8a19d4c14f/sdk/pluginapi/types.go#L1077-L1091)；[`RequestCompletion`](https://github.com/router-for-me/CLIProxyAPI/blob/20c9095cac089c6a8e40554debe4dd8a19d4c14f/sdk/pluginapi/types.go#L1197-L1226)；[`UsageRecord` 与 `UsageFailure`](https://github.com/router-for-me/CLIProxyAPI/blob/20c9095cac089c6a8e40554debe4dd8a19d4c14f/sdk/pluginapi/types.go#L1494-L1562)
- [U7] [Go SDK `Hook.OnResult`](https://github.com/router-for-me/CLIProxyAPI/blob/20c9095cac089c6a8e40554debe4dd8a19d4c14f/sdk/cliproxy/auth/conductor.go#L127-L135)
- [U8] [Codex 单次执行创建 reporter](https://github.com/router-for-me/CLIProxyAPI/blob/20c9095cac089c6a8e40554debe4dd8a19d4c14f/internal/runtime/executor/codex_executor_execute.go#L21-L40)；[非 2xx 返回错误](https://github.com/router-for-me/CLIProxyAPI/blob/20c9095cac089c6a8e40554debe4dd8a19d4c14f/internal/runtime/executor/codex_executor_execute.go#L130-L139)
- [U9] [失败发布与每次尝试上报](https://github.com/router-for-me/CLIProxyAPI/blob/20c9095cac089c6a8e40554debe4dd8a19d4c14f/internal/runtime/executor/helps/usage_helpers.go#L519-L584)；[账户字段与失败信息构造](https://github.com/router-for-me/CLIProxyAPI/blob/20c9095cac089c6a8e40554debe4dd8a19d4c14f/internal/runtime/executor/helps/usage_helpers.go#L640-L692)
- [U10] [usage 异步入队与分发](https://github.com/router-for-me/CLIProxyAPI/blob/20c9095cac089c6a8e40554debe4dd8a19d4c14f/sdk/cliproxy/usage/manager.go#L498-L586)
- [U11] [usage 字段映射到动态插件](https://github.com/router-for-me/CLIProxyAPI/blob/20c9095cac089c6a8e40554debe4dd8a19d4c14f/internal/pluginhost/adapters_usage_translation.go#L186-L216)；[发送 `usage.handle`](https://github.com/router-for-me/CLIProxyAPI/blob/20c9095cac089c6a8e40554debe4dd8a19d4c14f/internal/pluginhost/rpc_client.go#L605-L608)
- [U12] [Codex HTTP 流式 reporter 与失败处理](https://github.com/router-for-me/CLIProxyAPI/blob/20c9095cac089c6a8e40554debe4dd8a19d4c14f/internal/runtime/executor/codex_executor_stream.go#L21-L148)
- [U13] [额度错误分类与恢复时间解析](https://github.com/router-for-me/CLIProxyAPI/blob/20c9095cac089c6a8e40554debe4dd8a19d4c14f/internal/runtime/executor/codex_executor_terminal.go#L402-L444)

## 验证边界

本次仅做源码与公开源文件核验；未修改业务代码，未部署插件，未调用生产 Management API 或消耗真实 Reset Bank credits，未执行测试/构建。接入 `usage.handle` 后仍须验证重试切号、重复通知、普通限流、周窗口确认、流式错误与重启场景。
