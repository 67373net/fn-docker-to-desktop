# 把端口放到桌面 (put-port-on-desktop)

**飞牛OS (fnOS) 原生桌面图标管理、端口实时监控与反向代理网关一体化系统**

将 `wild-monitor`、`watchcow` 与 `watchcow-proxy` 深度融合的全新高性能项目。

---

## 核心特性

- **极致性能与超低资源占用**：
  - **纯 Go 标准库开发**：零外部庞大框架依赖，静态编译单个轻量二进制。
  - **常驻内存仅 ~8MB**，空闲 CPU 占用 **< 0.1%**。
  - **Docker 生产镜像仅 7.4MB**（基于极简 Alpine）。
  - **零打包前端**：纯原生 HTML5 + CSS3 + ES6 JavaScript，首屏极速秒开。
- **复刻 [wild-monitor] 核心功能**：
  - **本机端口与进程实时监控**：流式解析 Linux 内核 `/proc/net` 与 `/proc/[pid]`，聚合展示 TCP/UDP、IPv4/IPv6 端口、进程属主、CPU%、物理内存 RSS、磁盘 I/O。
  - **Docker 容器智能识别**：直连 Docker Socket，自动映射容器名称、服务与镜像。
  - **系统资源概览**：实时统计宿主机 CPU、内存、磁盘与网络实时吞吐。
  - **产品自身桌面图标自适应**：开箱即自动在飞牛OS桌面注册本产品图标；支持自定义自身界面是在**飞牛内部弹窗 (iframe)** 还是在**浏览器新标签页 (url)** 打开；支持设置图标是**仅管理员可见**还是**所有用户可见**。
- **复刻 [watchcow + watchcow-proxy] 核心功能**：
  - **本机现有端口一键放桌面**：在端口列表中点击“放到桌面”，即刻在飞牛OS桌面生成官方原生应用图标。
  - **局域网/广域网服务反向代理到本机并放桌面**：内置超高性能动态反向代理引擎，支持将 PVE、OpenWrt、NAS、打印机等任意 LAN/WAN 服务代理到本机端口并生成桌面图标，支持一键连通性测试、智能可用端口推荐、自签名证书忽略（跳过 TLS 校验）以及 WebSocket 自动透传。
  - **网页快捷方式图标**：支持添加任意外部网址为桌面单纯快捷方式图标。
  - **桌面图标统一管理**：统一查看、编辑、启用/停用、测试以及移出桌面。

---

## 界面与交互规范

- **严格去 Emoji 化**：除特别指定外，全站界面坚决不使用任何 emoji，全面采用 Feather/Lucide 风格线稿 SVG 图标。
- **100% 满屏宽度**：无最大宽度限制，充分利用大屏与窗口宽度。
- **自适应系统主题**：默认跟随操作系统明暗模式（Light / Dark 自动切换）。
- **清爽高信息密度**：文字简洁清晰，去除多余冗长副标题与干扰元素。
- **标准表格规范**：表头全部左对齐，列与列之间保持 2 个中文字符间隔，最右列边宽度不满时在右侧自然留空，表头宽度拉满。

---

## 快速启动

### 方式一：Docker Compose（推荐）

在项目根目录下执行：

```bash
# 启动服务
docker compose up -d

# 查看状态与资源占用
docker compose ps
docker stats --no-stream put-port-on-desktop
```

打开浏览器访问：
```
http://<宿主机IP>:5900 (或 5910)
```

### 方式二：使用 Makefile

```bash
make up       # 启动服务
make rebuild  # 快速增量重构
make logs     # 查看实时运行日志
make status   # 查看容器状态
make down     # 停止服务
```

---

## 项目结构

```
put-port-on-desktop/
├── icon.png                    # 产品高清图标 (512x512 PNG)
├── docker-compose.yml          # Docker Compose 配置文件
├── Dockerfile                  # 多阶段极速构建 Dockerfile (BuildKit 缓存加速)
├── Makefile                    # 快捷开发与部署控制脚本
├── go.mod                      # 零外部三方依赖 (Go 1.22+)
├── .env.example                # 环境变量配置模板
├── .gitignore                  # Git 忽略规则
├── CONVERSATION_HISTORY.md     # 完整对话历史、技术架构与 Token 消耗审计
├── cmd/
│   └── server/
│       └── main.go             # 服务主入口
├── internal/
│   ├── api/                    # RESTful API、SSE 事件流与静态资源路由
│   ├── auth/                   # HMAC 签名轻量认证鉴权系统
│   ├── desktop/                # fnOS 应用包生成器、appcenter-cli 调度与持久化
│   ├── monitor/                # 内核 procfs 采集、进程统计与 Docker 映射
│   └── proxy/                  # 原生动态反向代理管理器与网络连通性探测
├── web/
│   ├── embed.go                # go:embed 打包静态资源
│   ├── index.html              # 前端主结构 (100% 满屏响应式布局)
│   ├── style.css               # 极简线稿风、系统主题自适应样式
│   └── app.js                  # 原生 ES6 前端应用逻辑 (零三方库)
└── data/                       # 持久化数据存储 (桌面配置、图标、fnOS应用包)
```

---

## RESTful API 清单

| 方法 | 路径 | 说明 |
|------|------|------|
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
| `POST` | `/api/icons/upload` | 上传自定义图标图片 (PNG/JPG/WebP/SVG/ICO) |

---

## 许可证

MIT License
