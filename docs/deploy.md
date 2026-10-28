# 部署

chatchat 部署在 only Homelab，站点 `https://chatchat.onlyquant.top`，只在内网和 WireGuard 下可访问；网站登录走 Authelia OIDC，CLI 凭用户 token 访问 API。Compose、Caddy、frpc 的配置在 dotfiles 仓库（服务名 `chatchat`），本文记录服务器上要手动准备的部分和上线后的核对项。

## 镜像

- 推送 `v*` tag 时，[release-image.yml](../.github/workflows/release-image.yml) 和 CLI 的 [release-cli.yml](../.github/workflows/release-cli.yml) 同时运行。镜像发布到 `ghcr.io/ericwvi/chatchat:<版本>`（tag 去掉 `v`，如 `0.1.1`），正式版同时移动 `latest`，预发布版（`0.2.0-rc.1`）不动。服务端报告的版本是 `v<版本>`，和同一 tag 的 CLI 一致。
- 也可以在 Actions 页面手动触发，输入版本号，构建所选 ref。
- 平台 `linux/amd64`、`linux/arm64`。[Dockerfile](../Dockerfile) 在 runner 上构建前端（含类型检查）、检查嵌入完整（`scripts/check-embed.sh`）、跑 `go test ./...`，再交叉编译 `CGO_ENABLED=0` 的静态二进制；运行镜像是 distroless `static-debian12:nonroot`（uid 65532），监听 36760，数据在 `/app/data`，配置读 `/app/config.yaml`。
- 不在本地构建镜像。首次发布后确认 GHCR 包允许匿名拉取（否则在包设置里改成 Public）。

## 服务器准备

以下在 only 上以 root 操作，路径沿用 `/root/only/selfhost/<service>` 的习惯。

1. 目录：

   ```bash
   mkdir -p /root/only/selfhost/chatchat/data
   chown -R 65532:65532 /root/only/selfhost/chatchat
   ```

2. `/root/only/selfhost/chatchat/config.yaml`（只读挂载，不含密钥）：

   ```yaml
   listen: ":36760"
   data_dir: "/app/data"
   public_url: "https://chatchat.onlyquant.top"
   oidc:
     issuer: "https://auth.onlyquant.top"
     client_id: "chatchat"
   summary:
     base_url: "https://api.deepseek.com"
     model: "deepseek-flash"
   ```

   `limits` 用默认值；需要调整时参照 [config.example.yaml](../config.example.yaml)。不要设置 `dev_user`。

3. Compose 用的 `.env`（和 `ASR_OIDC_CLIENT_SECRET` 等放在一起）加两项，值不入 Git：

   - `CHATCHAT_OIDC_CLIENT_SECRET`：第 4 步生成的 client secret 明文
   - `CHATCHAT_SUMMARY_API_KEY`：DeepSeek API key

4. Authelia：在 `/root/only/selfhost/authelia/configuration.yml` 的 `identity_providers.oidc.clients` 下加 client。先生成 secret 和它的哈希：

   ```bash
   docker exec authelia authelia crypto hash generate pbkdf2 --variant sha512 --random --random.length 72 --random.charset rfc3986
   ```

   输出里的 `Random Password` 填到 `.env` 的 `CHATCHAT_OIDC_CLIENT_SECRET`，`Digest` 填到下面的 `client_secret`：

   ```yaml
         - client_id: chatchat
           client_name: chatchat
           client_secret: '$pbkdf2-sha512$…'
           public: false
           authorization_policy: one_factor  # 改成 tingxie client 的值
           consent_mode: implicit
           require_pkce: true
           pkce_challenge_method: S256
           redirect_uris:
             - https://chatchat.onlyquant.top/auth/callback
           scopes:
             - openid
             - profile
             - email
           grant_types:
             - authorization_code
           response_types:
             - code
           token_endpoint_auth_method: client_secret_basic
   ```

   字段同 Authelia 契约测试（`internal/auth/testdata/authelia/configuration.yml`）；`authorization_policy` 和 tingxie client 取同一个值。改完重启 authelia。

5. frpc：在 `/root/only/selfhost/memos/frpc.toml` 加 `chatchat` 的 `https` 代理（和 dotfiles `Linux/shanghai/frpc.toml` 的脱敏副本一致），重启 frpc 容器。

6. 更新 `/root/dotfiles` 拿到 Compose 和 Caddyfile 的改动，按平常的方式 `up -d chatchat`，再 reload Caddy。reload 后如果新站点出现 TLS 错误，重启 caddy 容器（agents 上线时遇到过，重启后恢复）。

## 上线核对

1. `docker logs chatchat` 有 `listening`，没有 `summaries are off` 和 `dev_user` 警告。
2. 在家和外出（WireGuard）都能打开 `https://chatchat.onlyquant.top`，跳 Authelia 登录后回到站点；`/agent.md` 不登录也能打开，里面的地址是 `https://chatchat.onlyquant.top`。
3. 网站生成用户 token，本机：

   ```bash
   chatchat config set-token --server https://chatchat.onlyquant.top   # 从 stdin 第一行读 token
   chatchat config check
   ```

4. 新建 chat，网页和 CLI 互发消息；凑满摘要阈值（默认 20 条文字消息）或让 CLI 参与者 `leave`，确认 DeepSeek 生成的摘要五节齐全、内容可信。
5. 让一个全新的 Agent session 只读 `https://chatchat.onlyquant.top/agent.md` 加入试一次。

## 升级与数据

- 升级：发新 tag，等镜像发布后改 Compose 里的镜像版本，`docker compose up -d chatchat`。服务端是单实例，等待通知和摘要任务都在进程内，重启时进行中的长轮询会断开，CLI 和网页自行重试；摘要任务状态重置为 `idle`，启动时重新判断。
- 数据只有 `/root/only/selfhost/chatchat/data` 下的 SQLite，属于每天的 restic 目录级备份范围（未做导出快照）。chat 最长存活 24 小时，丢失最近的数据只影响进行中的 chat；用户 token 和账号级历史 metadata 是需要保留的部分。
