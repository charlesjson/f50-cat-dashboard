# F50 猫猫增强面板

基于 [zashboard](https://github.com/Zephyruso/zashboard) 的 F50 / UFI-TOOLS 猫猫控制面板，补充设备上的持久化配置管理。前端沿用 zashboard 的代理选择、测速、连接、日志、规则和主题功能，新增“配置”页面。

## 功能

- 多订阅导入、节点池合并、启停、编辑及定时更新；下载失败继续使用上次成功缓存。
- 完整 Clash YAML 导入、上传、编辑、校验及切换。
- VMess、VLESS、Shadowsocks、Trojan、Hysteria2、TUIC 分享链接和 Base64 订阅解析，全部在设备本地完成。
- 手动节点、节点池启停和使用 `dialer-proxy` 的多跳链式代理。
- 自定义域名 / IP / 端口分流、DNS、GEO 资源与常用运行设置。
- 最终配置预览和下载、核心重载与重启、DNS / Fake IP 缓存清理。
- 应用前使用设备现有 Mihomo 内核校验，自动备份最近 20 次配置，运行时加载失败自动恢复。
- 并发版本检查、外部修改检测和中断事务恢复，避免覆盖其他工具写入的配置。

## 安装

需要已安装并运行的猫猫 TProxy 插件、ARM64 Mihomo 内核，以及可用的 root 后台。现有目录应为 `/data/clash`，前端为 `/data/clash/Proxy/WebUI/zashboard`，控制器使用 7788 端口且已设置非空 `secret`。

1. 从 [Releases](https://github.com/charlesjson/f50-cat-dashboard/releases) 下载 `f50-cat-dashboard-arm64.tar.gz`，并对照 `SHA256SUMS` 校验。
2. 上传至 F50，解压后通过 root shell 执行：

```sh
mkdir -p /data/local/tmp/f50-dashboard
cd /data/local/tmp/f50-dashboard
tar -xzf /path/to/f50-cat-dashboard-arm64.tar.gz
/system/bin/sh install.sh
```

打开 `http://设备IP:7788/ui/#/config`。控制面板连接密码仍为设备 Mihomo 配置中的 `secret`，无需设置新密码。

首次安装保留原有节点、策略组和路由；安装器只将面板更新源指向本项目，并部署管理服务。安装过程中保留原前端、配置和开机脚本；备份目录由安装器输出。

`dist.zip` 只包含前端，供已经安装管理服务的设备更新面板。首次安装请使用完整 ARM64 安装包。

## 使用说明

“配置 → 节点 → 导入订阅”提供两种方式：

- **节点池**：提取订阅中的节点，并合并到当前配置的第一个手动选择组。节点名称增加订阅名前缀，避免多个订阅重名。
- **完整配置**：保存订阅自带的策略组与规则，在“完整配置”列表点击“使用”后生效。支持直接上传本地 YAML。

手动节点可一次粘贴多个分享链接。链式代理选择入口、出口，流量先经过入口再从出口访问目标；原始节点保持独立。循环和缺失依赖会在应用前被拒绝。

完整模式保留原有路由，简单模式使用一个选择组和基础局域网直连规则。自定义路由优先匹配。切换模式、删除内容和恢复备份都可以通过自动备份撤回。

如果其他工具改写了配置，管理服务会停止继续写入；可在“高级 → 采用设备当前配置”建立新的基础配置，旧管理记录保存在备份中。

F50 依赖猫猫维护的 TProxy、DNS 监听和防火墙配置，因此这些参数以及控制器地址、密码、UI 路径不会被外部订阅覆盖。管理界面中的运行设置可以持久化保存；上游 zashboard 其他页面的运行时开关遵循 Mihomo 原有行为。

## 架构与认证

```text
浏览器 ── :7788/ui/ ── zashboard 前端
       ├─ :7788      ── Mihomo 控制器 API
       └─ :7790/api/ ── f50-manager ── 配置校验、缓存、备份、热重载
```

管理服务使用 Go 编写，无 Node.js / Python 运行依赖。服务只监听 IPv4，并要求局域网来源、现有控制器 Bearer 密钥和匹配设备主机的 CORS 来源。配置与备份保存在设备私有目录，权限为 `0700` / `0600`；没有第三方订阅转换服务。

管理数据：`/data/clash/Manager`。服务入口：

```sh
/data/clash/Scripts/Manager.Service start
/data/clash/Scripts/Manager.Service stop
/data/clash/Scripts/Manager.Service status
```

## 开发与验证

需要 Node.js 24+、pnpm（版本见 `package.json`）、Go 1.23+、Python 3（仅打包 ZIP）。

```sh
pnpm install --frozen-lockfile
pnpm type-check
pnpm exec eslint src/assembly/f50.ts src/views/ConfigPage.vue
cd manager
go test -race ./...
go vet ./...
cd ..
sh scripts/package-f50.sh
```

产物位于 `artifacts/`，包含完整安装包、前端 ZIP 和 SHA256 校验文件。CI 自动执行类型检查、变更代码 lint、管理服务测试和打包。

测试覆盖分享链接与传输参数、含多参数 URL 的 curl 下载、多订阅重名合并、链式依赖及循环、完整配置的设备参数保护、订阅失败缓存、配置校验失败、过期版本 / 外部修改、运行时回滚、事务恢复、后台更新与面板编辑并发，以及 API 访问控制。

## 来源与许可

前端基于 Zephyruso / zashboard 3.30.0 的 MIT 许可源码，来源版本见 [NOTICE](NOTICE)，保留 [LICENSE](LICENSE) 及上游鸣谢。新增管理服务与设备集成同样采用 MIT 许可。

本仓库不包含任何真实节点、订阅地址、设备口令或设备备份。自动更新核心会改变运行环境，请通过上游面板现有的核心管理功能按需操作；本安装器复用设备现有内核。
