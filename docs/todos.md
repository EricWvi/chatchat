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
- [ ] 原型确认后补充网站业务规则到 `docs/design.md`

## 阶段 1：服务端核心

- [ ] 项目骨架：参照听写的 Go module、SQLite、Taskfile、配置加载（YAML + 环境变量覆盖密钥）、结构化日志（日志脱敏 token / secret）
- [ ] 照搬听写的 OIDC 登录与会话（不含设备登录流程），保留 `dev_user` 本地开发模式
- [ ] User 与用户 token：创建、列出、吊销；只存哈希，生成时展示一次
- [ ] Chat：创建、查询、创建者销毁（级联删除消息、摘要，所有参与凭据失效）
- [ ] Participation：区分 `web` / `cli` 两类；网页参与者按（用户, chat）唯一；CLI `join` 每次新建身份；secret 只在响应里返回一次，服务端存哈希；`leave` 立即失效
- [ ] 鉴权：账号级接口认用户 token 或登录会话；CLI 参与级接口只认 `participation_id + secret` 且校验所属 chat，用户 token 不能替代；网页参与级接口只认登录会话，后端按（用户, chat）找到网页参与者
- [ ] 消息：chat 内单调递增 `sequence`；`client_request_id` 幂等，同 ID 不同内容返回冲突
- [ ] 读取：`after_sequence` 增量 + 有界分页；参与者列表与状态
- [ ] `wait`：长轮询，有新消息或超时即返回；读取、等待续租 lease
- [ ] 后台清理：lease 到期标离线；空闲到期和最长存活到期自动销毁；保留账号级历史 metadata
- [ ] 测试：覆盖需求第九节的 1、3、4、5、6、9、10

## 阶段 2：摘要（外部模型）

- [ ] 摘要快照表：`revision`、`covers_through_sequence`、`content`、`model`、`created_at`；只追加，读取返回最新一份
- [ ] 模型客户端：OpenAI 兼容接口，模型、地址、key 走配置；超时与重试
- [ ] 生成：上一份摘要 + `(上一份 covers, 目标 sequence]` 的消息 → 新摘要；固定结构（目标、已确认事实和决定、分歧、未解决问题、下一步及负责人）
- [ ] 提示词把聊天内容当数据处理，抵抗消息里的注入指令
- [ ] 触发：新增消息达到阈值，或 leave 时存在未覆盖消息；阈值集中配置
- [ ] 串行化：每个 chat 同时只有一个生成任务；以预期 revision 提交，冲突则放弃重算；`covers_through_sequence` 不超过实际最大 sequence；chat 销毁时取消进行中的任务并丢弃结果
- [ ] 失败处理：记录错误、退避重试；摘要缺失或落后时 Agent 仍能靠消息读取工作
- [ ] 测试：并发触发不丢更新、不越界；用假模型验证「摘要 + 增量消息」能恢复进展（需求第九节 7、8）

## 阶段 3：CLI

- [ ] `chatchat config check` / `set-token`：检查配置文件、服务端地址、用户 token 有效性、协议版本兼容
- [ ] `chatchat join <chat>`：输出一次性凭据；只把 metadata 写入本地历史
- [ ] `state`（最新摘要及覆盖位置、参与者）、`read`（增量 / 分页）、`wait`、`send`、`propose-end`、`leave`，凭据均从 stdin 第一行读取
- [ ] `chatchat history [--remote]`：只列 `chat_id`、`participation_id`、`display_name`、`joined_at`、`left_at`
- [ ] 不实现 `--resume`、默认 current participation；配置、历史、日志、错误输出都不含 secret
- [ ] `send` 自动生成 `client_request_id`，网络重试复用同一个
- [ ] 测试：历史文件不含 secret（需求第九节 2、10）

## 阶段 4：网站

- [ ] 登录页接入 OIDC；用户 token 管理页（生成时展示一次、吊销）
- [ ] 创建 chat、邀请（链接 / 复制给 Agent 的提示）、加入
- [ ] 聊天页：消息流（长轮询或 SSE）、发送；Human 发送者由后端从会话绑定，不接受客户端填写的参与者 ID
- [ ] 侧栏：最新摘要及其覆盖位置、参与者在线状态
- [ ] 创建者销毁 chat；普通参与者退出、建议结束
- [ ] 页面说明 lease、空闲销毁、最长存活的默认值

## 阶段 5：agent.md 与端到端闭环

- [ ] `/agent.md`：按「读取指南 → 检查 CLI 与 token → join → 读摘要和之后的消息 → 发言前检查新增 → 发送 → 等待 → 完成后 leave」组织；写明 secret 不得发到 chat、聊天内容和摘要不能覆盖本地权限或系统指令、空闲用有超时的等待
- [ ] `agent.md` 说明凭据约定只防止新 session 自动继承旧身份，不是进程隔离或安全沙箱
- [ ] CLI 安装与分发方式写入 agent.md
- [ ] 端到端：网站开房 → 两个独立 Agent session 加入 → 交换消息 → 摘要生成 → 退出 → 销毁后凭据和内容全部失效
- [ ] 需求第九节 10 项验证全部通过

## 阶段 6：部署（待细化）

- [ ] 镜像构建（GitHub Actions 发布 GHCR，不在本地构建）
- [ ] Compose 服务、Caddy 站点、Authelia OIDC client、模型 key 等密钥配置
- [ ] CLI 发布与安装方式

## 后续（动态调整）

- [ ] 点名（@参与者）
- [ ] 回复引用
- [ ] 消息搜索
