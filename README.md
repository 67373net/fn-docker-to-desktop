# 把 Docker 放到桌面 (fn-docker-to-desktop)

**飞牛OS (fnOS) 原生应用：将 Docker 容器、本机端口与网络服务一键放置在飞牛桌面**

> 本项目灵感来源于 [watchcow](https://github.com/tf4fun/watchcow)。

---

## 核心特性

- **飞牛OS (fnOS) 原生应用 (`.fpk`) 架构**：
  - 直接在飞牛「应用中心 -> 手动安装」中安装 `.fpk` 包，由系统原生常驻守护运行。
  - **飞牛统一网关 (Unified Gateway) 零端口占用**：全面支持飞牛统一网关 Unix Domain Socket (`gatewaySocket: "app.sock"`)，安装时**无需配置或输入任何端口号**，彻底杜绝宿主机端口冲突与端口不同步问题。
  - 拥有宿主机 Root 运行权限，直调 `/usr/bin/appcenter-cli` 原生注册桌面快捷入口，动态绑定宿主机端口，直连 `/var/run/docker.sock`。
- **极致性能与超低资源占用**：
  - **纯 Go 标准库开发**：零庞大外部框架依赖，单个轻量静态编译二进制。
  - **常驻内存仅 ~8MB**，空闲 CPU 占用 **< 0.1%**。
  - **零打包纯净前端**：原生 HTML5 + CSS3 + ES6 JavaScript，首屏极速秒开。
- **容器与端口监控（复刻 wild-monitor）**：
  - **实时端口与进程状态**：流式解析 Linux 内核 `/proc/net` 与 `/proc/[pid]`，聚合展示 TCP/UDP、IPv4/IPv6 端口、进程属主、CPU%、物理内存 RSS、磁盘 I/O。
  - **Docker 容器智能识别**：直连 Docker Socket，自动映射容器名称、服务状态与镜像；默认优先筛选 Docker 容器。
  - **多选维度筛选**：支持在 Docker 容器、宿主原生、TCP、UDP 间多维度组合筛选，智能处理“全部”逻辑。
  - **宿主机资源概览**：实时统计 CPU、内存、磁盘与网络实时吞吐。
  - **自身桌面图标自适应**：安装后即刻在飞牛OS桌面出现本产品图标；原生通过飞牛统一网关安全隔离运行，无需端口占用；支持设置自身图标是**仅管理员可见**还是**所有用户可见**。
- **服务与快捷方式上桌面（融合 watchcow + watchcow-proxy）**：
  - **Docker 容器与本机端口一键放桌面**：在端口列表中点击高亮“放到桌面”，即刻在飞牛OS桌面生成官方原生应用图标。
  - **同一端口多桌面快捷方式**：支持为同一个端口创建多个不同路径（如 `/admin`）或名称的桌面图标，已创建图标支持一键查看、列表管理与“另存为新图标”。
  - **局域网/广域网服务反向代理到本机并放桌面**：内置超高性能动态反向代理引擎，支持将 PVE、OpenWrt、NAS、打印机等任意 LAN/WAN 服务代理到本机端口并生成桌面图标，支持一键连通性测试、智能可用端口推荐、自签名证书忽略（跳过 TLS 校验）以及 WebSocket 自动透传。
  - **网页快捷方式图标**：支持添加任意外部网址为桌面单纯快捷方式图标。
  - **桌面图标统一管理与即时开关**：内置 iOS 风格滑动开关，随时启用/停用桌面图标（停用时桌面图标隐去，启用时立刻恢复），支持编辑与移出桌面。
- **8 天本地滚动日志与启动诊断**：
  - **系统级日志记录**：自动按天轮转记录于文件系统 `${TRIM_PKGVAR}/logs/app-YYYY-MM-DD.log` 及 `${TRIM_PKGVAR}/info.log`。
  - **8 天智能自清理**：后台自动修剪清理超过 8 天的历史日志文件，保持磁盘整洁。
  - **产品界面日志视窗**：Web 界面内置终端风格日志查看器，支持按日期切换、级别过滤（全部/信息/警告/错误）、关键字高亮检索、自动刷新与一键下载。
  - **启动自检诊断与防闪退**：启动时自动记录详细环境诊断（进程 PID、UID、系统版本、端口绑定、飞牛环境变量），集成 Panic 崩溃拦截与完整堆栈追踪，彻底杜绝无声闪退。

---

## 界面与交互规范

- **严格去 Emoji 化**：全站界面坚决不使用任何 emoji，全面采用通用标准线稿 SVG 图标。
- **100% 满屏宽度**：无最大宽度限制，充分利用大屏与窗口宽度。
- **自适应系统主题**：默认跟随操作系统明暗模式（Light / Dark 自动切换）。
- **清爽高信息密度**：文字简洁清晰，去除多余冗长副标题与干扰元素。
- **标准表格规范**：表头全部左对齐，列与列之间保持 2 个中文字符间隔，最右列边宽度不满时在右侧自然留空，表头宽度拉满。

---

## 安装与使用

### 1. 飞牛OS后台安装

1. 从 Release 下载编译好的 `fn-docker-to-desktop-x86.fpk`（或 ARM 架构 `fn-docker-to-desktop-arm.fpk`）。
2. 打开飞牛OS后台，进入「**应用中心**」。
3. 点击右上角「**手动安装**」，上传 `.fpk` 文件（统一网关模式，无需配置任何端口）。
4. 安装完成后，飞牛桌面上将立即出现「**把 Docker 放到桌面**」图标。

### 2. 开发者本地构建

```bash
# 构建飞牛OS .fpk 安装包 (默认 x86_64)
make fpk
# 或者：
./scripts/build-fpk.sh x86

# 构建 ARM64 架构安装包
./scripts/build-fpk.sh arm

# 本地直接编译二进制
make build

# 清理构建临时产物
make clean
```

---

## 目录结构

```
fn-docker-to-desktop/
├── icon.png                    # 产品高清图标 (512x512 PNG)
├── Makefile                    # 快捷构建控制脚本
├── go.mod                      # Go 模块定义
├── go.sum                      # Go 校验和
├── docs/
│   └── CONVERSATION_HISTORY.md # 完整对话历史、技术架构与 Token 消耗审计
├── fnos-app/                   # 飞牛OS原生应用包定义
│   ├── manifest                # 应用元数据 (提供者 67373net、Root 权限声明、统一网关)
│   ├── ICON.PNG                # 飞牛桌面图标 (64x64)
│   ├── ICON_256.PNG            # 飞牛桌面高清图标 (256x256)
│   ├── cmd/                    # 飞牛服务生命周期脚本 (main, install, uninstall)
│   ├── config/                 # 权限 (privilege) 与资源 (resource) 配置
│   ├── wizard/                 # 安装向导 (零端口配置)
│   └── app/ui/config           # 桌面图标启动方式配置 (iframe / url, gatewaySocket)
├── scripts/
│   └── build-fpk.sh            # 官方规范 fpk 打包脚本 (app.tgz + checksum)
├── .github/workflows/
│   └── build.yaml              # GitHub Actions CI/CD 多架构自动编译与 Release 发版
├── cmd/server/main.go          # 后台常驻服务主入口
├── internal/
│   ├── api/                    # RESTful API、SSE 事件流与静态资源路由
│   ├── auth/                   # HMAC 签名轻量认证鉴权系统
│   ├── desktop/                # 飞牛桌面图标生成器、appcenter-cli 调度与持久化
│   ├── monitor/                # 内核 procfs 采集、进程统计与 Docker 映射
│   └── proxy/                  # 原生动态反向代理管理器与网络连通性探测
└── web/
    ├── embed.go                # go:embed 静态资源打包
    ├── index.html              # 前端主结构 (100% 满屏响应式布局)
    ├── style.css               # 极简线稿风、系统主题自适应样式
    └── app.js                  # 原生 ES6 前端应用逻辑 (零三方库)
```

---

## RESTful API 清单

| 方法 | 路径 | 说明 |
| :--- | :--- | :--- |
| `GET` | `/api/ports` | 获取本机所有端口及进程、Docker 绑定信息 |
| `GET` | `/api/processes` | 获取全系统进程运行状态及资源消耗 |
| `GET` | `/api/system` | 获取系统 CPU、内存、磁盘与网络实时指标 |
| `GET` | `/api/host` | 获取宿主机硬件信息与网卡列表 |
| `GET` | `/api/events` | SSE 实时事件推送流 |
| `GET` | `/api/desktop/items` | 获取所有已创建的桌面图标列表 |
| `POST` | `/api/desktop/items` | 创建桌面图标 (本机端口 / 代理服务 / 快捷方式) |
| `PUT` | `/api/desktop/items/{id}` | 修改指定桌面图标配置 |
| `DELETE` | `/api/desktop/items/{id}` | 移出桌面图标并同步卸载 fnOS 应用 |
| `POST` | `/api/desktop/items/{id}/toggle` | 启用 / 停用桌面图标 |
| `GET` | `/api/settings` | 查询自身桌面展示方式与配置 |
| `POST` | `/api/settings` | 保存设置并实时同步更新飞牛桌面图标 |
| `POST` | `/api/proxy/test` | 测试目标后端连通性与 HTTP 延迟 |
| `GET` | `/api/ports/available` | 智能推荐未占用端口 |
| `GET` | `/api/logs` | 查询系统滚动日志（支持日期切换、级别过滤与搜索） |
| `GET` | `/api/logs/download` | 下载指定日期的原始日志文本文件 |

---

## 常见问题 FAQ

#### Q: 安装时系统提示「该应用由 未知发布者 提供」，是否需要注册开发者或配置证书？
> **答**：不需要注册。这是飞牛OS (fnOS) 对所有离线手动上传安装的第三方应用包 (`.fpk`) 的标准安全保护提醒（类似于 Windows 的未知发布者提示或 macOS 的未签名应用提示）。目前飞牛官方开放平台（`https://developer.fnnas.com/`）的开发者注册与应用市场上架审核通道尚处于建设中（官方标注 Coming Soon），所有离线分发的社区 FPK 包均会显示此提醒，点击「同意」或「继续安装」即可正常使用。

#### Q: 日志在文件系统中的具体存储路径在哪里？
> **答**：
> - 每日轮转日志：`${TRIM_PKGVAR}/logs/app-YYYY-MM-DD.log`（即 `/vol1/@appstore/fn-docker-to-desktop/var/logs/app-YYYY-MM-DD.log`）
> - 进程标准输出与守护日志：`${TRIM_PKGVAR}/info.log`
> - 日志在本地自动保留 **8 天**，超过 8 天的历史日志会在后台自动清理。

---

## 许可证

MIT License
