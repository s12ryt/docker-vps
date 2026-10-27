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
| `SHELL` | `/bin/bash` | PTY 內啟動的 shell（轉譯層啟用時指 rootfs 內路徑） |
| `SESSION_TTL` | `12h` | 登入 session 有效期（Go duration 格式，如 `30m`、`24h`） |
| `DATA_ROOT` | *自動* | 見下節：`/data → /` 持久化轉譯層。顯式指定根目錄；未設時自動檢測 `/data` |

## `/data → /` 持久化轉譯層（雲託管 VPS 存儲）

Docker 雲託管商通常**只允許掛載 `/data` 之類的指定路徑**，不允許掛載根 `/`。
本鏡像內建轉譯層：終端裡看到的 `/` 實際由 `<DATA_ROOT>/rootfs` 承載，
所有寫入（`/root`、`/home`、`/etc`、`apt install` 裝的軟件……）都落在持久卷上，**重啟不丟**。

```bash
# 雲商場景：掛了 /data 即自動生效，無需任何環境變量
docker run -d -p 7681:7681 -v your-volume:/data ghcr.io/s12ryt/docker-vps

# 本地測試 / 自定義掛載點：顯式指定
docker run -d -p 7681:7681 -v mydata:/data -e DATA_ROOT=/data ghcr.io/s12ryt/docker-vps
```

**行為細節**：

- 首次啟動：自動把鏡像根目錄白名單（bin etc home lib lib64 media mnt opt root sbin srv usr var）複製到 `/data/rootfs`，之後**不再覆蓋**——你的修改和已裝軟件就是 VPS 的「磁盤」
- `/proc` `/sys` `/dev` 由 [proot](https://proot-me.github.io/) 自動綁定（用戶態實現，**無需任何特權**，雲託管容器可用）
- 未掛 `/data` 且未設 `DATA_ROOT` 時轉譯層自動禁用，行為與舊版完全一致
- `DATA_ROOT` 設置但初始化失敗（如只讀卷）→ 容器直接報錯退出，不會靜默降級讓你誤以為已持久化
- 想重置「磁盤」：刪掉卷內的 `rootfs/` 目錄後重啟即可重新鋪底

> 效能提示：proot 通過 ptrace 攔截系統調用，常規 shell 操作無感；大量小文件 IO（如大型 `git clone`）會比原生慢，屬預期權衡。

## 功能

- 🖥️ 瀏覽器內完整終端（xterm.js + WebSocket + PTY，支援 vim / tmux / 顏色）
- 💾 `/data → /` 轉譯層：雲商只給掛 `/data` 也能當整台 VPS 用，數據全持久
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
├── Dockerfile                  # multi-stage：Go 交叉編譯 → debian-slim（含 proot）
├── docker-compose.yml          # 本地運行示例
├── .github/workflows/docker-image.yml  # tag 觸發 → GHCR 多架構
└── webshell/                   # 自構建網頁 Shell（Go + xterm.js）
    ├── main.go                 # 入口（含 DATA_ROOT 解析與 rootfs 初始化）
    ├── config.go               # 環境變量解析（PORT/PW/SHELL/TTL/DATA_ROOT）
    ├── rootfs.go               # /data→/ 轉譯層：rootfs 白名單複製（冪等）
    ├── auth.go                 # 登入、session 保護、暴力破解鎖定
    ├── session.go              # 內存 session 存儲（256-bit token）
    ├── server.go               # 路由 + embed 靜態資源
    ├── ws.go                   # WebSocket ↔ PTY 橋接（含 proot 包裝）
    └── web/                    # 前端（xterm.js，MIT）
```

## 安全須知

- 容器內 shell 以 **root** 運行（VPS 用途），請勿暴露到不受信網絡
- 建議置於反代（TLS）之後使用，或至少設置強 `PW`
- session 存於內存：容器重啟即全部登出
