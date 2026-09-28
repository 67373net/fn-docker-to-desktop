# AGENTS.md - fn-docker-to-desktop 项目上下文与开发规范

本文件为 Antigravity Agent 及其他 AI 编程助手的常驻记忆与行为规范基准。在新会话启动或切换任务时，Agent 必须首先遵循本文档所定义的规范。

---

## 1. 项目概况与架构定位

- **项目名称**：`fn-docker-to-desktop`（把 Docker 放到桌面）
- **适用平台**：飞牛 OS (fnOS)，运行于 Root 权限。
- **核心定位**：监控本机端口、Docker 容器以及远程主机网络服务，一键将服务注册为飞牛原生桌面应用（原生弹窗/新标签页打开），支持外网 FN Connect 穿透代理与网页快捷方式。
- **技术栈**：
  - **后端**：Go (标准库 HTTP 路由、反向代理、Docker SDK、Linux /proc 与内核套接字分析、系统日志采集)。
  - **前端**：原生 HTML5 / CSS3 / Vanilla JavaScript（无臃肿前端框架打包），采用 Server-Sent Events (SSE) 实时单向流更新状态。
  - **打包与部署**：飞牛 OS `.fpk` 原生应用包规范，通过 GitHub Actions 自动化构建与发布。

---

## 2. 历史记录与演进记忆 (最高优先级参考)

- **演进记录文件**：[`docs/CONVERSATION_HISTORY.md`](docs/CONVERSATION_HISTORY.md)
- **要求**：
  - 项目历经数十个版本的快速迭代（已记录至 `Turn 93` / `v1.1.77`）。
  - 处理涉及开屏弹窗、端口映射、外网穿透权限、图标管理等复杂逻辑时，**必须先阅读该文档中最近版本的相关条目**，了解此前修复的技术背景，严禁反复引发历史已修复的问题。
  - 每次完成发布或重要修改后，必须严格遵循相同的极简格式在 [`docs/CONVERSATION_HISTORY.md`](docs/CONVERSATION_HISTORY.md) 末尾追加记录。

---

## 3. 严格执行的铁律规范

### 3.1 零 `.fpk` 文件残留
- **绝对禁止**将编译生成的 `.fpk` 文件提交到 Git 仓库中；
- 每次提交代码前，必须执行检查确认无任何 `.fpk` 残留（发布包由 GitHub Actions 云端构建）。

### 3.2 更新日志与 GitHub Release 极简原则
- **严格遵循**：更新日志只写“修改与修复”的内容，**字越少越好，绝对不写任何废话、口水话或多余解释**。
- **无需包含版本号**：Release 标题及 Git Tag 已带版本号，更新日志内严禁/无需包含“全链路版本升级至 vX.Y.Z”等冗余行。
- **格式范例**：
  ```markdown
  ### 修改与修复
  - 修复 xxx 导致的 yyy 问题
  - 优化 zzz 交互逻辑
  ```

### 3.3 全链路版本号同步 (5 处联动，缺一不可)
版本升级时，必须全局搜索并同步更新以下所有位置：
1. `cmd/server/main.go`：`const appVersion = "X.Y.Z"`
2. `fnos-app/manifest`：`version = X.Y.Z`
3. `internal/api/handler_test.go`：测试用例断言中的版本号
4. `web/index.html`：
   - 样式表查询参数：`<link rel="stylesheet" href="style.css?v=X.Y.Z">`
   - 关于页面展示：`<span id="about-app-version">vX.Y.Z</span>`
   - 脚本文件查询参数：`<script src="app.js?v=X.Y.Z"></script>`
5. `web/app.js`：
   - 多处 `state.settings?.version || 'X.Y.Z'` 回退默认值。

---

## 4. 核心业务逻辑与避坑指南

### 4.1 开屏弹窗 (Notice Modal) 与外网独立域名
- **共存方案**：本地容器若开启开屏提示，系统会自动为其分配独立的本地反向代理端口，确保每个容器能够生成唯一的飞牛 App 标识，实现**既有开屏弹窗又有独立外网穿透域名**。
- **弹窗触发机制**：使用单次进入标记（One-Time Access Flag）结合请求来源 Host 判定。
  - 未勾选“今日不再提示”时：每次从飞牛桌面点击打开**必须弹出**；
  - 弹窗内点击“进入应用”后：在当前应用内正常页面跳转、表单刷新**不得重复打扰**。

### 4.2 桌面图标权限体系
- 飞牛 OS 桌面应用配置文件中必须显式写入可见权限：
  - `allUsers: true` 时全员可见；
  - `allUsers: false` 时仅管理员可见。
- **避坑**：缺少全员权限声明会导致外网通过 FN Connect 访问时报错 `“FN Connect 暂无权限访问该服务”`。

### 4.3 目标地址分段式交互规范
- 本机端口与端口映射均采用统一的单行分段输入栏：
  `[协议下拉: http/https] :// [主机输入] : [端口号] / [访问路径]`
- **本机端口模式**：主机写死锁定为 `localhost`（不可编辑，灰色只读），端口必填；
- **端口映射模式**：主机输入 IP 或域名，支持直接粘贴完整 URL（前端自动智能拆分填入各段），端口可选留空，支持“测试连通”；
- 移除端口映射中冗余的圆圈感叹号帮助按钮，保持排版极简紧凑。

---

## 5. 项目目录结构索引

- `cmd/server/`：程序启动入口与静态资源嵌入。
- `internal/api/`：HTTP RESTful API 接口定义与路由处理。
- `internal/desktop/`：飞牛桌面图标注册、桌面应用生成、Docker 事件监控。
- `internal/proxy/`：反向代理流、开屏提示注入器、自签 TLS 证书跳过逻辑。
- `internal/storage/`：本地 JSON 存储引擎与设置持久化。
- `web/`：前端 SPA 页面、CSS 主题样式、业务逻辑与分段表单。
- `fnos-app/`：飞牛 OS 安装包元数据、图标与应用配置定义。
- `docs/`：项目深度演进文档与收款码。
