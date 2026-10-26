- 工作过程中临时文件放在各级工作目录的 .tmp 下，例如 clone 的源码等。
- 脚本所需依赖也放到 .tmp 下，例如 Python venv。
- 工作区下如果有 memories，先读 memories/AGENTS.md，再按其指引了解与当前项目有关的记忆。
- 工作区下如果有 specs，先读 specs/AGENTS.md。

## Commands

Project commands live in `Taskfile.yml`; run `task --list` to see them all.
Prefer the smallest relevant task while iterating, and run `task test` before
considering a change complete. `task` loads secrets from `.env`; never print
config values or environment variables that may hold them.

- Format Go sources: `task format`
- Go gofmt check and vet: `task lint:go`
- Go tests: `task test:go`
- All lint and unit tests: `task test`
- Authelia contract test (needs a container socket and local images): `task test:contract`
- Release server binary (static, `CGO_ENABLED=0`): `task build`
- Local server with `config.yaml` (set `dev_user` to skip OIDC): `task run:server`
