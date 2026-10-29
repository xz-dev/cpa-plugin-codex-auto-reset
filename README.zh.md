# Codex Auto Reset

[English](README.md) | 简体中文

`codex-auto-reset` 是一个 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)（CPA）插件，代你兑换 Codex「Reset Bank」重置额度。

Codex/ChatGPT 账户会积累限速重置额度。每个额度可以把账户的周限速窗口重置一次，并且每个额度都会过期。没人及时兑换的额度会作废，兑换时机不对的额度会被浪费。插件监控你启用的账户，只在规则允许时才用掉额度：

- 在最早过期的额度过期前兑换，避免作废；
- 可选：账户周额度用完时立即兑换——除非周额度马上就会自然恢复。

每次兑换都指定一个确切的额度，并在之后核实结果。插件无法确认发生了什么时，会停止该账户的自动兑换，而不是猜测。

插件以动态库形式运行在 CPA 内。它通过 CPA 的插件接口读取 Codex 凭据，直接访问 ChatGPT 后端；重置核实成功后，请求 CPA 清除该凭据的路由冷却，让流量立刻恢复。

## 功能

- **按账户设置策略。** 全局默认值加上启用/禁用账户列表；默认不启用任何账户。每个账户都可以单独覆盖任何一项全局默认值：巡检间隔、提前量、周耗尽兑换，以及恢复延迟和延迟窗口。覆盖项会显示继承的全局值，**恢复默认** 会清除覆盖项。
- **到期兑换。** 每次巡检（默认每 1 小时）找出最早过期的额度，记下它的 ID 和过期时间，在过期前一个提前量（默认 5 分钟）时兑换正好这一个额度。不会换用其他额度，目标过期后也绝不发送请求。
- **周耗尽兑换（可选）。** 最新的未取整读数显示周额度已用满（`used_percent >= 100`）时，插件兑换一个额度。普通 429、单独的 5 小时窗口、代码审查限额和未知用量都不会触发。
- **恢复延迟（可选）。** 如果同一周窗口会在设定窗口内（默认 24 小时，含边界）自然恢复，就跳过这次周耗尽兑换，避免把额度花在马上就会回来的配额上。到期触发仍然优先。上游没有给出可靠的恢复时间时，按普通耗尽规则处理。
- **核实消耗。** 兑换后插件重新读取额度列表。只有同一个额度 ID 消失，或状态为 `redeemed`/`consumed`/`used`、或带有 `redeemed_at` 时间，才算已消耗。成功还要求可用数量减一、周额度恢复。临时错误会用同一个幂等键重试，重试不会重复兑换。
- **暂停自动兑换。** 任何不明确的结果都会暂停该账户的自动兑换。巡检继续以只读方式进行，绝不做补偿性兑换。连续 3 次稳定的只读检查（每次间隔超过 30 分钟）后，或者某次已经用掉额度的兑换等到账户周额度恢复后，暂停会自动解除。
- **手动操作。** **立即检查** 马上执行一次只读巡检。**立即兑换** 在确认后兑换最早过期的额度；它不受恢复延迟限制，也是解除暂停的手动方式。
- **历史和日志。** 每次兑换都记录在重置统计面板中。最近的日志（最多 1000 条，保留 7 天）显示在页面上。
- **持久状态。** 设置、账户状态、历史和日志在 CPA 重启和容器重建后都会保留。状态文件不含任何令牌，保留一份备份；文件损坏时会隔离原文件，而不是覆盖它。
- **导出和导入。** 管理 API 可以把插件状态导出为 JSON 并重新导入。导入只恢复设置和历史，不恢复运行时的暂停状态。
- **管理页面** 支持英文和中文。它复用管理中心已保存的登录，自身不会主动查询上游。

所有状态、重试规则和安全规则见 [docs/reference.md](docs/reference.md)（英文）。

## 安装

从[最新 release](../../releases/latest) 下载 `codex-auto-reset_<version>_<goos>_<goarch>.zip`。提供 Linux、macOS 和 Windows 的 amd64 与 arm64 构建。把库文件复制到 CPA 对应平台的插件目录，例如：

```bash
mkdir -p /path/to/CLIProxyAPI/plugins/linux/amd64
cp codex-auto-reset.so /path/to/CLIProxyAPI/plugins/linux/amd64/
```

Linux 库需要 glibc 2.34 或更高版本；官方 CPA 镜像（Debian bookworm）为 2.36。在 CPA 的 `config.yaml` 中启用插件并重启 CPA：

```yaml
plugins:
  enabled: true
  configs:
    codex-auto-reset:
      enabled: true
      priority: 1
```

插件目录中只保留一个版本的插件。升级时替换库文件并重启 CPA，已保存的设置和历史会保留。

## 使用

从 CPA 的插件资源打开页面，或访问 `/v0/resource/plugins/codex-auto-reset/status`。带路径前缀（如 `/cpa/v0/resource/...`）也能使用。

先在管理中心登录并勾选「记住密码」。页面会复用同一 CPA 实例保存的登录；缺失或被拒绝时会显示提示。然后：

1. 设置全局默认值。时长填数字加分钟或小时单位；小于 1 分钟会被拒绝。
2. 把要管理的账户移到启用列表。
3. 按需覆盖账户的设置，并逐行保存。

页面每 15 秒刷新一次已保存的状态。

也可以在插件的 `config_yaml` 中配置；页面保存的值优先：

```yaml
refresh_interval: 1h                     # 巡检间隔
trigger_lead_time: 5m                    # 在过期前多久兑换
auto_reset_on_weekly_exhaustion: false   # 周额度用完时兑换
defer_reset_near_weekly_recovery: false  # ……除非它很快会自然恢复
weekly_recovery_guard_window: 24h        # 多久以内算「很快」（最少 1m）
enabled_accounts: [auth_index_1]
account_overrides:                       # 上面任一设置都可以按账户覆盖
  auth_index_1:
    trigger_lead_time: 10m
    auto_reset_on_weekly_exhaustion: true
    defer_reset_near_weekly_recovery: true
    weekly_recovery_guard_window: 12h
state_dir: /absolute/path                # 可选：指定状态文件位置
```

状态默认保存在 `<CPA auth 目录>/plugin-state/codex-auto-reset/state.data`。使用 Docker 时，auth 目录本来就要挂载（凭据在里面），状态也就一起保留了。

管理 API（所有路由都需要 CPA 管理密钥）见 [docs/reference.md](docs/reference.md#management-api)。

## 构建

要求：Go 1.26+、CGO 和 C 编译器。

```bash
make test
make build                  # dist/codex-auto-reset.so（或 .dylib/.dll）
make package VERSION=0.2.2  # 打包 release zip
```

Linux 库要能在官方 CPA 镜像中加载，就要在相同的基础镜像里编译，例如：

```bash
docker run --rm -v "$PWD":/src -w /src golang:1.26-bookworm make build VERSION=0.2.2
```

在 Windows 上，`scripts/build.ps1` 构建 `dist/codex-auto-reset.dll`。推送 `v*` 标签会在 GitHub Actions 上构建并发布 release 文件。

仓库结构：

- `cmd/codex-auto-reset/` — C ABI 入口（仅 cgo 胶水代码）
- `internal/plugin/` — 插件本体：状态机、worker、OpenAI 客户端、管理 API 和页面
- `internal/sim/` — 兑换状态机的确定性模拟
- `scripts/` — release 打包和 Windows 构建脚本
- `docs/` — [参考文档](docs/reference.md)和归档历史
- `openspec/` — 当前规范和已归档的变更

## 背景

CPA 在许多 Codex 账户之间路由请求，每个账户的重置额度各有各的过期时间。手动兑换很容易忘。兑换错额度、重复兑换，或者在配额马上就会自然恢复时提前兑换，都会浪费额度。因此插件只在规则允许时用掉额度，始终指定确切的额度，核实结果，并在无法确认发生了什么时停止自动兑换。

## 许可证

MIT，见 [LICENSE](LICENSE)。
