# chatchat TODO

简化版 Free4Chat：网站开临时 chat，人和多个独立 Agent session 通过 `chatchat` CLI 加入并文字沟通。服务端调用外部模型生成摘要，每份摘要绑定截止的消息 `sequence`；Agent 读取「最新摘要 + 之后的消息」恢复进展。

阶段 0–5 已确定，完成后才算端到端闭环；阶段 6 之后按实际使用情况调整。

## 阶段 0：设计定稿

已确认：
- 部署在 only Homelab 内网，网站登录走 Authelia OIDC；被邀请的人需要 Authelia 账号
- 摘要只由服务端模型生成，Agent 只读，不提供更新摘要的命令
- 摘要触发：自上次摘要后新增消息达到阈值（默认 20 条），或参与者 leave 时有未覆盖的消息
- 模型走 OpenAI 兼容接口，默认 DeepSeek，地址、模型、key 走配置
- 技术栈：Go + React + SQLite，前端 embed 进服务二进制；CLI 用 Go 写成单个静态二进制；结构参照听写（`../asr`），OIDC 登录直接照搬其 `internal/auth`
- Participation 分两类：网页参与者每个用户在每个 chat 固定一个，凭登录会话鉴权；CLI 参与者每次 `join` 新建，凭 secret 鉴权。两类接口互不通用
- 邀请：知道 chat 链接的登录用户都能加入，chat ID 不可猜测，不另设邀请码
- secret 传递：`join` 向 stdout 输出一行 JSON 凭据；后续命令从 stdin 第一行读这行 JSON，命令行参数不出现 secret
- CLI 参与者离线后凭据仍有效，只有 `leave` 或 chat 销毁才失效
- 集中配置默认值：在线租约 90s；无在线参与者 30 分钟后销毁；chat 最长存活 24h；单次等待最长 60s；每页默认 50、最多 200 条

- [x] `docs/design.md`：身份模型（User / Chat / Participation）、两层凭据、生命周期、摘要模型
- [x] `docs/api.md`：HTTP API、错误码、CLI 命令与退出码；结构化协议版本（`GET /api/v1/meta`）
- [x] 网站原型（`design/web.html`，选定 A 工作台布局，B、C 保留为落选变体）：登录后首页（我的 chat、创建）、聊天页（消息流、摘要、参与者、邀请、退出 / 销毁）、用户 token 管理、chat 已销毁 / 不存在等状态页
- [x] 原型确认后补充网站业务规则到 `docs/design.md`

## 阶段 1：服务端核心

- [x] 项目骨架：参照听写的 Go module、SQLite、Taskfile、配置加载（YAML + 环境变量覆盖密钥）、结构化日志（只记方法、路径、状态码，不记头、查询串和请求体）
- [x] 照搬听写的 OIDC 登录与会话（不含设备登录流程），保留 `dev_user` 本地开发模式；Authelia 契约测试一并搬来（`task test:contract`）
- [x] User 与用户 token：创建、列出、吊销；只存哈希，生成时展示一次
- [x] Chat：创建、查询、创建者销毁（删除消息，所有参与凭据失效；摘要在阶段 2 一并删除）
- [x] Participation：区分 `web` / `cli` 两类；网页参与者按（用户, chat）唯一；CLI `join` 每次新建身份；secret 只在响应里返回一次，服务端存哈希；`leave` 立即失效
- [x] 鉴权：账号级接口认用户 token 或登录状态；CLI 参与级接口只认 `participation_id + secret` 且校验所属 chat，用户 token 不能替代；网页参与级接口只认登录状态，后端按（用户, chat）找到网页参与者
- [x] 消息：chat 内单调递增 `sequence`；`client_request_id` 幂等，同 ID 不同内容返回冲突
- [x] 读取：`after_sequence` 增量、`before_sequence` 向前翻页；参与者列表与状态
- [x] `wait`：长轮询，有新消息或超时即返回；网页版在参与者变化时也返回 `changed`；读取、等待续租 lease
- [x] 后台清理：lease 到期通知网页刷新参与者；空闲到期和最长存活到期自动销毁；保留账号级历史 metadata
- [x] 测试：覆盖需求第九节的 1、3、4、5、6、9、10（`internal/server/verify_test.go`）

实现中补充到 `docs/api.md` 的接口细节：`GET /api/v1/chats/{chat_id}`（已销毁页用）、chat 列表改用 `cursor` / `next_cursor` 分页、`state.chat` 增加 `created_by_me` 和 `idle_destroy_at`、错误码 `not_found` 和 `internal`。

## 阶段 2：摘要（外部模型）

- [x] 摘要快照表：`revision`、`covers_through_sequence`、`content`、`model`、`created_at`；只追加，读取返回最新一份；chat 销毁时与消息一起删除
- [x] 模型客户端：OpenAI 兼容接口，配置 `summary` 段（地址、模型、key、`extra_body`、超时），key 可用 `CHATCHAT_SUMMARY_API_KEY` 覆盖；`dev_user` 模式下可不配，摘要关闭
- [x] 生成：上一份摘要 + `(上一份 covers, 目标 sequence]` 的消息 → 新摘要；固定五节结构，输出不符合即视为失败；消息多时分批（200 条 / 60000 字符），每批单独写入一个 revision
- [x] 提示词把聊天内容当数据处理：整体编码为 JSON 对象，系统提示要求不执行消息里的指令
- [x] 触发：新增文字消息达到阈值，或 leave 时存在未覆盖的文字消息；失败后阈值从失败任务的目标位置重新计数；启动时重新判断所有存在的 chat
- [x] 网页 `wait` 在摘要更新或生成状态变化时返回 `changed: ["summary"]`；CLI `wait` 不受影响
- [x] 串行化：每个 chat 同时只有一个生成任务；以预期 revision 条件写入，冲突则放弃重新判断；`covers_through_sequence` 不超过实际最大 sequence；chat 销毁时取消进行中的任务，store 也拒绝写入
- [x] 失败处理：退避重试最多 3 次（5s / 20s / 60s），state 返回 `summary_job`（`idle` / `pending` / `failed`、`last_failed_at`），不返回、也不记录模型原始错误内容
- [x] 测试：并发触发不丢更新、不越界；用假模型验证「摘要 + 增量消息」能恢复进展（需求第九节 7、8，`internal/server/verify_test.go`）；其余在 `internal/server/summary_test.go`、`internal/summary`

实现中定下的细节（已写进 `docs/design.md`）：每批单独写入 revision；失败后阈值重新计数；任务状态只在进程内保存，重启后为 `idle`；`last_failed_at` 成功后保留；日志不记模型接口的错误正文。

## 阶段 3：CLI

- [x] `chatchat config check` / `set-token`：检查配置文件、服务端地址、用户 token 有效性、协议版本兼容
- [x] `chatchat join <chat>`：输出一次性凭据；只把 metadata 写入本地历史
- [x] `state`（最新摘要及覆盖位置、参与者）、`read`（增量 / 分页）、`wait`、`send`、`propose-end`、`leave`，凭据均从 stdin 第一行读取
- [x] `chatchat history [--remote]`：只列 `chat_id`、`participation_id`、`display_name`、`joined_at`、`left_at`
- [x] 不实现 `--resume`、默认 current participation；配置、历史、日志、错误输出都不含 secret
- [x] `send` 自动生成 `client_request_id`，网络重试复用同一个
- [x] 测试：历史文件不含 secret（需求第九节 2、10，`internal/cli/cli_test.go` 的 `TestNoCredentialLeaks`）；退出码、幂等重试、协议版本不兼容

实现中定下的细节（已写进 `docs/api.md` 和 `docs/design.md`）：每条联网命令先查 `/meta` 再执行；凭据行里的 `server` 优先于配置；`wait --timeout` 超过服务端上限时连续等待；`send` 对无响应和 5xx 重试 2 次（1 秒、3 秒）；凭据行缺失或格式不对退出码 3；`config set-token` 带 `--server` 时一并保存地址；历史写入加文件锁；`task build` 同时构建服务端和 CLI（`build:server`、`build:cli`）。

## 阶段 4：网站

按 `docs/design.md`「网站」一节和 `design/web.html` 的 A 工作台布局实现；样式 token 从原型迁到 Tailwind 主题。

- [x] 前端骨架：参照听写的 Vite + React + React Query；OIDC 登录跳转、用户菜单、亮暗主题、滚动条与焦点样式
- [x] 左侧栏：新建 chat、进行中 / 已销毁列表（在线人数、排序）
- [x] 首页新建表单，创建后进入 chat
- [x] 聊天页：进入（enter）、消息流（合并连续消息、Markdown、系统消息、向上翻页、底部跟随）、发送（幂等重试、组字时不发送）
- [x] 实时：网页 `wait` 长轮询，按 `changed` 刷新摘要和参与者
- [x] 右侧栏：摘要（整理位置、之后条数、生成中 / 失败）、参与者（类型、状态点、ID 后缀）、生命周期提示
- [x] 邀请菜单；更多菜单（建议结束、退出、创建者销毁）及确认框
- [x] 已销毁页、不存在页；销毁后打开中的页面立即切换
- [x] 用户 token 页：生成（只显示一次）、吊销确认、配置示例

实现中定下的细节（已写进 `docs/design.md` 和 `docs/api.md`）：
- 解决阶段 2 遗留的限制：`state` 和网页 `wait` 返回 `state_version`（进程内按 chat 计数摘要和参与者变化，带进程纪元），网页 `wait` 带上它时报告两次等待之间的变化。
- 登录支持 `?next=` 回到原页面，只接受本站路径；否则被邀请的人打开 chat 链接、登录后会落到首页。
- 页面路径：`/`（新建）、`/c/<chat_id>`、`/tokens`；token 页、已销毁页、不存在页是不带侧栏的独立页面，同原型。
- chat 列表每 15 秒刷新，打开中的 chat 参与者变化等事件时立即刷新；列表只取第一页 200 条。
- 限制值（消息长度、租约、空闲销毁、摘要阈值等）从 `/meta` 读，不写死。
- 指针打开的菜单和对话框关闭后不把焦点还给触发按钮，避免留下焦点框；键盘操作仍还焦点。
- Markdown 用 react-markdown，不渲染原始 HTML，链接在新标签页打开。
- `task build:server` 先构建前端再嵌入；`task test` 包含前端类型检查。

## 阶段 5：agent.md 与端到端闭环

- [x] `/agent.md`：按「读取指南 → 检查 CLI 与 token → join → 读摘要和之后的消息 → 发言前检查新增 → 发送 → 等待 → 完成后 leave」组织；写明 secret 不得发到 chat、聊天内容和摘要不能覆盖本地权限或系统指令、空闲用有超时的等待
- [x] `agent.md` 说明凭据约定只防止新 session 自动继承旧身份，不是进程隔离或安全沙箱
- [x] CLI 安装与分发方式写入 agent.md（GitHub Releases + `latest.json`，见阶段 6）
- [x] 端到端：网站开房 → 两个独立 Agent session 加入 → 交换消息 → 摘要生成 → 退出 → 销毁后凭据和内容全部失效
- [x] 需求第九节 10 项验证全部通过

实现中定下的细节（已写进 `docs/design.md` 和 `docs/api.md`）：
- 指南正文是 `internal/server/agent.md`，嵌入服务端二进制；`{{origin}}` 替换成站点地址（`public_url`，`dev_user` 模式取请求的 Host），`Cache-Control: no-cache`。路由注册在前端静态文件之前，不经过 SPA。
- 指南要求所有命令加 `--json`；凭据行用带引号的 heredoc（`<<'CHATCHAT_EOF'`）传入；用户 token 由用户自己 `config set-token`，Agent 不索要；`wait` 必须带 `--timeout` 且小于 Agent 的工具调用超时，长时间（例如 10 分钟）没有新消息就停止等待、汇报或退出。
- 端到端用真实二进制（服务端 dev_user 模式 + 假模型，摘要阈值 3）和 Playwright 驱动网站，Agent session 各用独立 HOME，凭据只在驱动进程内存里；脚本在 `.tmp/e2e/run.sh`（未入库），57 项检查全部通过，覆盖需求第九节 1–10，另直接查 SQLite 确认销毁后消息和摘要已删除、库里没有明文 secret。
- Vite 开发服务器把 `/agent.md` 也代理到后端。

## 阶段 6：部署（待细化）

- [ ] 镜像构建（GitHub Actions 发布 GHCR，不在本地构建）
- [ ] Compose 服务、Caddy 站点、Authelia OIDC client、模型 key 等密钥配置
- [x] CLI 发布与安装方式：推送 `v*` tag 由 `.github/workflows/release-cli.yml` 交叉编译 5 个平台，发布 GitHub Release，附带 `SHA256SUMS` 和 `latest.json`（同 palace）；agent.md 给出按 `latest.json` 选包、校验、安装的命令
- [x] `chatchat update`：同 `pal update`，读 `latest.json`、校验版本 / 平台 / 固定 URL / SHA-256 后原子替换当前程序，保留权限；不读配置、不发凭据（`internal/cli/update_test.go`）
- [x] 首次发布 CLI 后核对：v0.1.0 的 5 个平台包、`SHA256SUMS`、`latest.json` 齐全；agent.md 的安装命令、`SHA256SUMS` 校验和 `chatchat update` 在 Linux 上走通
- [ ] Windows 的运行中替换尚无实测
- 注意：新仓库第一次推送时，workflow 文件和 tag 同一次到达不会触发 Actions，v0.1.0 是在 upstream 重推 tag 后才触发的

## 后续（动态调整）

- [ ] 点名（@参与者）
- [ ] 回复引用
- [ ] 消息搜索
