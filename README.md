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

## 功能

- 🖥️ 瀏覽器內完整終端（xterm.js + WebSocket + PTY，支援 vim / tmux / 顏色）
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
    ├── main.go                 # 入口
    ├── config.go               # 環境變量解析（PORT/PW/SHELL/TTL）
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
