# docker-vps — 自構建網頁 Shell 的 Linux 鏡像

基於 **Debian bookworm-slim** 的輕量容器，內建一個**從源碼自構建的網頁終端機（Web Shell）**。
打 git tag 時 GitHub Actions 自動構建 `linux/amd64` + `linux/arm64` 並推送至 GHCR。

## 快速開始

```bash
# 免密碼模式 — 打開 http://localhost:7681 即用
docker run -d -p 7681:7681 ghcr.io/s12ryt/docker-vps

# 密碼模式 — 先登入才能使用終端
docker run -d -p 7681:7681 -e PW=your-secret ghcr.io/s12ryt/docker-vps
```

## 環境變量

| 變量 | 默認 | 說明 |
|------|------|------|
| `PW` | *(空)* | **空 = 免密碼模式**；設置任何非空白值即啟用密碼登入 |
| `PORT` | `7681` | Web Shell 監聽埠 |
| `SHELL` | `/bin/bash` | PTY 內啟動的 shell |
| `SESSION_TTL` | `12h` | 登入 session 有效期（Go duration 格式，如 `30m`、`24h`） |
| `DATA_ROOT` | *自動* | 見下節：`/data` 持久層。顯式指定根目錄；未設時自動檢測 `/data` |

## `/data` 持久層（雲託管 VPS 存儲）

Docker 雲託管商通常**只允許掛載 `/data` 之類的指定路徑**（且根分區可能只有 1GB）。
本鏡像內建自構建持久層：掛載 `/data` 後，容器根的 `/usr` `/var` `/root` `/home` `/opt` `/etc`
以 symlink 遷到 `<DATA_ROOT>/rootfs`——**shell 原生跑在容器根**（apt、tmux、htop、PTY 全部正常、
零翻譯開銷），所有系統狀態與寫入都落在持久卷上，**重啟/重建容器不丟**。

```bash
# 雲商場景：掛了 /data 即自動生效，無需任何環境變量
docker run -d -p 7681:7681 -v your-volume:/data ghcr.io/s12ryt/docker-vps

# 本地測試 / 自定義掛載點：顯式指定
docker run -d -p 7681:7681 -v mydata:/data -e DATA_ROOT=/data ghcr.io/s12ryt/docker-vps
```

**行為細節**：

- 首次啟動：把鏡像根的 `/usr`（約 600MB）`/var` `/root` `/home` `/opt` 複製到 `/data/rootfs`，並把容器根原目錄替換為指向卷的 symlink；`/etc` 採混合模式（目錄保留、逐項 symlink，`resolv.conf`/`hostname`/`hosts` 三個 Docker 管理文件除外）。首啟需複製 ~600MB，HEALTHCHECK 的 start-period 已設 5 分鐘，請耐心等 healthy
- 之後**卷優先**：卷內已有的數據永不會被鏡像層覆蓋——你的修改和 `apt install` 裝的軟件就是 VPS 的「磁盤」
- 鏡像升級語義：換新版鏡像後系統以**卷內狀態為準**（舊套件保留），要更新系統請在終端裡 `apt update && apt upgrade`；webshell 本體在 `/app`（永不遷移），新版永遠生效
- 從 v1.1.0（proot 版）帶卷升級：自動兼容，卷內既有數據原樣保留
- 未掛 `/data` 且未設 `DATA_ROOT` 時持久層自動禁用，行為與無持久層版本完全一致
- `DATA_ROOT` 設置但初始化失敗（如只讀卷）→ 容器直接報錯退出，不會靜默降級讓你誤以為已持久化
- 想重置「磁盤」：刪掉卷內的 `rootfs/` 目錄後重啟即可重新鋪底

## 功能

- 🖥️ 瀏覽器內完整終端（xterm.js + WebSocket + PTY，支援 vim / tmux / 顏色）
- 💾 `/data` 持久層：雲商只給掛 `/data` 也能當整台 VPS 用，apt 裝的軟件與數據全持久、原生性能
- 🔐 `PW` 變量決定是否需要密碼；session cookie（HttpOnly + SameSite=Strict）
- 🛡️ 暴力破解防護：同一來源連續 5 次密碼錯誤鎖定 30 秒
- 📦 內建常用工具：bash / curl / vim / git / tmux / htop / procps / net-tools / iproute2 / openssh-client
- ❤️ `HEALTHCHECK` → `GET /healthz`（恆為 200，供編排系統探活）
- 🔁 斷線自動重連、視窗大小自適應（resize 消息）
- 🧹 tini 作為 PID 1，轉發信號並回收殭屍 shell 進程

## 發布流程（自動構建）

```bash
git tag v1.0.0
git push origin v1.0.0
# → GitHub Actions 構建 linux/amd64 + linux/arm64 → ghcr.io/<owner>/<repo>:1.0.0 與 :1.0
```

- 僅 `v*` tag 觸發構建；普通 push 不觸發
- 首次發布後請在 GitHub 倉庫 `Settings → Actions → General → Workflow permissions` 勾選 **Read and write permissions**（workflow 已聲明 `packages: write`，通常無需額外配置）

## 本地開發

```bash
# 跑測試（單元測試在任何平台；PTY 集成測試需 Linux）
cd webshell && go test ./...

# 本地構建
docker build -t docker-vps:local .

# 或用 compose
docker compose up -d
```

## 項目結構

```
├── Dockerfile                  # multi-stage：Go 交叉編譯 → debian-slim
├── docker-compose.yml          # 本地運行示例
├── .github/workflows/docker-image.yml  # tag 觸發 → GHCR 多架構
└── webshell/                   # 自構建網頁 Shell（Go + xterm.js）
    ├── main.go                 # 入口（含 DATA_ROOT 解析與持久層初始化）
    ├── config.go               # 環境變量解析（PORT/PW/SHELL/TTL/DATA_ROOT）
    ├── rootfs.go               # /data 持久層：白名單目錄遷移 symlink（冪等）
    ├── auth.go                 # 登入、session 保護、暴力破解鎖定
    ├── session.go              # 內存 session 存儲（256-bit token）
    ├── server.go               # 路由 + embed 靜態資源
    ├── ws.go                   # WebSocket ↔ PTY 橋接
    └── web/                    # 前端（xterm.js，MIT）
```

## 安全須知

- 容器內 shell 以 **root** 運行（VPS 用途），請勿暴露到不受信網絡
- 建議置於反代（TLS）之後使用，或至少設置強 `PW`
- session 存於內存：容器重啟即全部登出
