# 把 Docker 放到桌面 (fn-docker-to-desktop)

本项目灵感及代码参考了项目 [watchcow](https://github.com/tf4fun/watchcow)。

本项目由一个三流代码吟唱师用三流 agent 开发，可能会有亿点点 bug 和调整。欢迎提 [issues](https://github.com/67373net/fn-docker-to-desktop/issues)

---

### 主要功能

将 Docker / 局域网服务 / 公网服务放到飞牛桌面，一键打开，并可利用飞牛 connect 连接。

**功能说明：**

1. **Docker 一键放桌面**：自动识别容器，一键生成飞牛桌面图标。
2. **局域网 / 公网服务反向代理**：可自动或通过 SSH 扫描其他机器端口，将内网或公网服务代理至本机并生成桌面图标。
3. **网页快捷方式**：将任意网址放置到桌面。
4. **兼容 Watchcow 标签**：自动读取 Docker Compose 中的 Watchcow label 配置，可以直接启用或复制。

其他特色：文字图标生成器，开屏弹窗备忘，同一端口多入口，可忽略自签证书，图标上传 / 管理与备份恢复，等等。

---

### 使用方式

前往 [Releases](https://github.com/67373net/fn-docker-to-desktop/releases) 界面下载最新版本的 `.fpk` 安装包，在飞牛 OS「应用中心 -> 手动安装」中上传即可。

---

### 常见问题与提示

- **1Panel / Portainer 等面板添加到桌面后打不开？**
  1. **打开方式**：此类运维面板具备防嵌套机制，只能选择**“新标签页”**打开；
  2. **安全入口**：若开启了安全入口，请在添加时将路径填写完整（如 `/1panel`）；
  3. **外网穿透**：通过 FN Connect 外网访问时，需在面板设置中将穿透域名加入允许访问白名单。

---

### 🙇🏻 请作者吃顿外卖 🥺

「把 Docker 放到桌面」是一款开源免费应用。如果它为您带来了便利与价值，欢迎扫描下方二维码投喂支持，感谢您的认可与鼓励！☕

| 微信支付 | 支付宝 |
| :---: | :---: |
| <img src="docs/微信收款码.jpg" width="260" alt="微信收款码"> | <img src="docs/支付宝收款码.jpg" width="260" alt="支付宝收款码"> |

---

### 开源许可

本项目采用 [MIT License](LICENSE) 许可证。
