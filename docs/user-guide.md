# sandboxd 使用手册

> 面向"sandboxd 怎么用"。先读 [README.md](../README.md) 了解项目定位;安全与生产注意事项见 [production-safety.md](production-safety.md);架构细节见 [ARCHITECTURE.md](../ARCHITECTURE.md)。
>
> **Claude 助手使用本工具:** 走 `~/.claude/skills/sandboxd/SKILL.md`(凭证从 `~/.claude/sandboxd.env` 读),它是这份手册的精简速记版。

---

## 0. 它是什么(30 秒读完)

sandboxd 跑在**单台 Linux 主机**上,把每个用户的代码放到一个**独立容器**里运行,通过 Traefik 给出 `s-<id>-<port>.preview.<domain>` 的预览 URL,空闲自动停、需要时自动醒。控制平面是**一个 Go 二进制**,状态用 SQLite,网络用 Traefik,没有 K8s、没有独立 DB。

```
POST /sandbox                 → 起一个沙箱(给端口、给 env)
POST /sandbox/{id}/exec       → 在沙箱里跑命令
POST /v1/sandboxes/{id}/tasks → 在沙箱里让 AI 代理写代码
http://s-<id>-<port>.preview.<domain>  → 访问沙箱里 :port 的 dev server
```

---

## 1. 安装

### 1.1 前置
- Linux 主机,**Docker Engine + Compose 插件**(`docker compose` 可用)
- 当前用户能跑 Docker(在 `docker` 组里,或能用 `sudo`;`install.sh` 会自动检测并加 `sudo`)

### 1.2 一条命令安装
```bash
git clone https://github.com/tastyeffectco/sandboxd.git
cd sandboxd
./install.sh
```

`install.sh` 干了什么:
1. 检测 Docker / Compose,无权限时自动用 `sudo`
2. 没有 `.env` 就从 `.env.example` 复制(默认值就跑得起来)
3. 创建数据目录 `SANDBOXD_DATA_DIR`(默认 `/var/lib/sandboxed`,包含工作区 + SQLite + 日志)
4. 构建基础镜像 `sandboxd-base:1.0.0`(**首次几分钟,之后走缓存**)
5. 构建并启动 `sandboxd-control-plane` + `traefik`(`docker compose up -d`)
6. 打印 API 地址和首次创建沙箱的命令

> 脚本是**幂等**的:可重复跑,不会动工作区。
> **不会**安装 systemd 单元或系统包,容器靠 Docker `restart: unless-stopped` 自动拉起。

### 1.3 验证装好了
```bash
curl -s http://127.0.0.1:9090/healthz   # -> ok
curl -s http://127.0.0.1:9090/readyz    # -> ready
```

### 1.4 改安装后的端口
如果 80 已被占(常见:Rancher Desktop / Docker Desktop 自带 k3s 抢 80),在 `.env` 里:
```bash
HTTP_PORT=8088
```
然后 `docker compose up -d traefik`。预览 URL 变成 `http://s-<id>-port>.preview.localhost:8088`。

---

## 2. 核心概念

| 概念 | 含义 |
|---|---|
| **沙箱 ID** | ULID。创建时 `id` 字段可省,自动生成;自己传必须合法 ULID,否则 `id must be a ULID` |
| **暴露端口** | 创建时 `ports:[3000,3001]`(3000=用户 dev server,3001=agents-ui);`auth_ports:[3001]` 强制 3001 走 forward-auth |
| **预览 URL** | `http://s-<id>-<port>.preview.${PREVIEW_DOMAIN}:${HTTP_PORT}`。浏览器把 `*.localhost` 解析到 `127.0.0.1`,本地零配置 |
| **工作区** | 沙箱内 `/home/sandbox`,宿主机 `<DATA_DIR>/workspaces/<id>`,**bind-mount,持久化** |
| **`runtimed`** | 沙箱内常驻进程,提供 `/tasks` HTTP API + unix socket,只有控制平面能连它 |
| **空闲策略** | `sleep`(默认,空闲 35 min 自动 stop) / `always_on`(永不自动停) |

---

## 3. 核心 API

Base URL = `http://${SANDBOXD_API_BIND}`,默认 `http://127.0.0.1:9090`。**默认无鉴权**(`SANDBOXD_API_AUTH_DISABLED=true`),生产环境务必改 `false` 并设 `SANDBOXD_API_TOKENS=name:secret`,传 `-H "Authorization: Bearer <secret>"`。

### 3.1 沙箱生命周期

| 方法 & 路径 | 请求体 | 作用 | 关键行为 |
|---|---|---|---|
| `POST /sandbox` | `{"id"?, "ports":[3000], "env":{...}?}` | 创建 | `env` 注入到容器环境(给代理 API key 用),**会进容器内 `/proc/1/environ`** |
| `GET /sandboxes` | — | 列出所有 | |
| `GET /sandbox/{id}` | — | 取一个 | 响应中**不含** `env` |
| `POST /sandbox/{id}/exec` | `{"cmd":["bash","-lc","..."]}` | 跑命令 | **非交互**(无 TTY、无 stdin) |
| `POST /sandbox/{id}/keepalive` | — | 推迟 idle reaper | 不让 35 min 计数清零 |
| `POST /v1/sandboxes/{id}/stop` | — | 立即 stop,下次预览请求自动唤醒 | 释放 RAM |
| `DELETE /sandbox/{id}` | — | 删容器,**保留**工作区 | |
| `POST /sandbox/{id}/purge` | — | 删容器 + 删工作区 | 不可逆 |

### 3.2 文件与任务

| 方法 & 路径 | 请求体 | 作用 |
|---|---|---|
| `PUT /v1/sandboxes/{id}/files` | `{"path":"...","content":"...","append":false}` | 写文件到工作区(限 25 MiB,禁 `..` 与保留子树) |
| `GET /v1/sandboxes/{id}/files` | — | 列工作区文件 |
| `GET /v1/sandboxes/{id}/files/content?path=...` | — | 读文件 |
| `POST /v1/sandboxes/{id}/tasks` | `{"prompt":"...","agent":"opencode"}` | 提交编码任务给 `runtimed` |
| `GET /v1/sandboxes/{id}/tasks/{taskId}` | — | 取结果 |
| `GET /v1/sandboxes/{id}/tasks/{taskId}/events` | — | **SSE 实时流**,`curl -N` 读 |
| `POST /v1/sandboxes/{id}/tasks/{taskId}/cancel` | — | 取消任务 |

### 3.3 健康检查
- `GET /healthz` → 进程活(`ok`)
- `GET /readyz` → 能调 Docker daemon(`ready`)

---

## agents-ui 控制面板

每个沙箱都自带一个 Web 控制面板 ([claude-code-cli-ui](https://github.com/Ngxba/claude-code-cli-ui)),
管理 `~/.claude/` 下的 agents / commands / skills / workflows / plugins。
它是 Claude Code CLI 的伴侣,不是替代品 —— 两者可以同时用。

**访问地址**: `http://s-<id>-3001.preview.<PREVIEW_DOMAIN>`

**约定**:
- 3000 = 用户自己的 dev server(不强制认证)
- 3001 = agents-ui 控制面板(总是过 `sandbox-preview-auth` forward-auth,即使沙箱 visibility=public)

**创建示例**:
```bash
ID=$(curl -fsS -XPOST $SANDBOXD_API/sandbox \
     -H 'content-type: application/json' \
     -d '{"ports":[3000,3001],"auth_ports":[3001],"env":{"ANTHROPIC_API_KEY":"sk-ant-..."}}' \
     | sed -E 's/.*"id":"([^"]+)".*/\1/')
echo "dev server:    http://s-$ID-3000.preview.localhost"
echo "agents-ui:     http://s-$ID-3001.preview.localhost"
```

**注意**: `auth_ports:[3001]` 总是把 3001 走 forward-auth —— 即使你把 visibility 设为 public。
**不要把 3000 写成 agents-ui**:`auth_ports` 不传 3000 是有意为之 —— 用户的 dev server
不应该被强制认证拦截。

---

## 4. 端到端示例(从零到一个能访问的预览)

```bash
API=http://127.0.0.1:9090

# 1. 创建一个暴露 3000 + 3001 端口的沙箱(3001 = agents-ui 控制面板,走 forward-auth)
ID=$(curl -s -XPOST $API/sandbox -H 'content-type: application/json' \
       -d '{"ports":[3000,3001],"auth_ports":[3001]}' | sed -E 's/.*"id":"([^"]+)".*/\1/')
echo "sandbox=$ID"
echo "dev server:    http://s-$ID-3000.preview.localhost"
echo "agents-ui:     http://s-$ID-3001.preview.localhost"

# 2. 在沙箱里起一个 dev server
curl -s -XPOST $API/sandbox/$ID/exec -H 'content-type: application/json' \
  -d '{"cmd":["bash","-lc","cd ~/workspace && echo hello > index.html && python3 -m http.server 3000"]}'

# 3. 用 preview 域名访问(本地 *.localhost → 127.0.0.1)
curl -s -H "Host: s-$ID-3000.preview.localhost" http://127.0.0.1/

# 4. 手动 stop,演示唤醒
curl -s -XPOST $API/v1/sandboxes/$ID/stop
curl -s -H "Host: s-$ID-3000.preview.localhost" http://127.0.0.1/   # 第一次返回"Spinning up"并自动刷新
# 再次请求就直连 dev server 了

# 5. 删工作区
curl -s -XPOST $API/sandbox/$ID/purge
```

---

## 5. 让 AI 代理写代码(核心卖点)

镜像里**预装**了 `opencode`(开箱即用免费档)和 `claude`(`claude.ai/install.sh` 安装)。注入 API key 有三种方式,推荐第一种。

### 5.1 创建时注入(推荐)
```bash
ID=$(curl -s -XPOST $API/sandbox -H 'content-type: application/json' \
       -d '{"ports":[3000],"env":{"ANTHROPIC_API_KEY":"sk-ant-..."}}' \
       | sed -E 's/.*"id":"([^"]+)".*/\1/')

curl -s -XPOST $API/v1/sandboxes/$ID/tasks -H 'content-type: application/json' \
  -d '{"prompt":"build a Vite todo app and run it on port 3000","agent":"opencode"}'

# 流式看进度
curl -N $API/v1/sandboxes/$ID/tasks/<taskId>/events
```
代理工作目录是 `~/workspace/app`。

### 5.2 一次性 exec(适合临时)
```bash
curl -s -XPOST $API/sandbox/$ID/exec -H 'content-type: application/json' \
  -d '{"cmd":["bash","-lc","ANTHROPIC_API_KEY=sk-ant-... claude -p \"write hello.py\""]}'
```

### 5.3 交互式(本机)
```bash
docker exec -it -e ANTHROPIC_API_KEY=sk-ant-... s-$ID bash
# 容器内:claude   (或 opencode)
```

---

## 6. 预览 URL 的工作机制(为什么会自动醒来)

第一次访问 `http://s-<id>-3000.preview.<domain>` 时的链路:

1. 浏览器 → Traefik,匹配 `s-<id>-3000.preview.<domain>`
2. 沙箱**已停** → 没有 priority-100 路由 → Traefik 的 priority-1 catch-all 命中,转发到 `sandboxd:9000`
3. `sandboxd` 检查 wake admission(内存余量)、`docker start` 沙箱、TCP 探测端口、返回一个 "Spinning up" 页面(自带 meta-refresh)
4. 沙箱起来后,Traefik 自动发布 priority-100 路由
5. 第二次刷新,直接打到沙箱的 dev server

> 想看 Spinning up 页面长啥样,源码在 `control-plane/internal/wake/html.go`。

---

## 7. 常用操作清单

```bash
# 看控制平面日志
docker compose logs -f sandboxd

# 看 stack 状态
docker compose ps

# 重启控制平面
docker compose restart sandboxd

# 列出所有沙箱(在主机层)
docker ps --filter label=sandboxd.managed=true

# 改 idle 阈值(停得更快/更慢)
echo "SANDBOXD_IDLE_THRESHOLD_SECONDS=600" >> .env
docker compose up -d sandboxd
```

---

## 8. 配置项速查(`.env`)

完整列表见 [`.env.example`](../.env.example),常用三件套:

| 变量 | 默认 | 何时改 |
|---|---|---|
| `HTTP_PORT` | `80` | 80 被占时 |
| `SANDBOXD_API_BIND` | `127.0.0.1:9090` | 9090 被占;暴露给 LAN 时改 `0.0.0.0:9090` + 必开鉴权 |
| `PREVIEW_DOMAIN` | `localhost` | 公网部署时改为真实通配域名 |
| `SANDBOXD_DATA_DIR` | `/var/lib/sandboxed` | 想换到别的盘就改这个,host/container 路径必须一致 |
| `SANDBOXD_API_AUTH_DISABLED` | `true` | 上公网/给别人用 → `false` + 配 `SANDBOXD_API_TOKENS=name:secret,name2:secret2` |
| `SANDBOXD_IDLE_THRESHOLD_SECONDS` | `2100`(35 min) | 想让空闲停得快/慢 |
| `SANDBOXD_SET_MEMORY_HIGH` | `false` | 软限 cgroup,需要主机 cgroup 访问,默认不开 |
| `PREVIEW_TLS` | `false` | 见 README "Production / TLS",需要通配证书 + DNS-01 |

> 改完 `.env` 跑 `docker compose up -d` 让新值生效。

---

## 9. 故障排查

| 现象 | 原因 / 处理 |
|---|---|
| `readyz` 不返回 `ready` / 报 `docker info: exit status 1` | 控制平面摸不到 Docker socket;确认 compose 里 `/var/run/docker.sock` 已挂载,daemon 正在跑 |
| 预览 URL 全 404 | 80 端口被抢;`HTTP_PORT=8080` + `docker compose up -d traefik` |
| `id must be a ULID` | 自传了非 ULID 的 `id`,去掉 `id` 字段自动生成 |
| 预览页停在 "Spinning up your app…" | 沙箱刚唤醒,或端口上没东西在监听 |
| 创沙箱时 seeding/permission 错 | 主机开了 `userns-remap`;保留 `SANDBOXD_USERNS=host` 默认 |
| `uninstall` 后 `install` 报数据目录非空 | 正常,工作区还在;`uninstall --data` 才删,或手动 `rm -rf /var/lib/sandboxed` |

---

## 10. 卸载

```bash
./uninstall.sh            # 停 stack + 删所有沙箱 + 删网络(保留工作区)
./uninstall.sh --images   # 顺带删构建好的镜像
./uninstall.sh --data     # 顺带删工作区 + SQLite(**会问 yes**)
./uninstall.sh --all      # 上面两个一起
./uninstall.sh --all -y   # 不问直接干
```

安全保证:只删带 `sandboxd.managed=true` 标签的容器,不动别的;不动 git 仓库本身。

---

## 一页 cheat sheet

```bash
# 起服务
./install.sh

# 创建一个 3000+3001 端口、带 Anthropic key 的沙箱,跑一个任务,流式看进度
ID=$(curl -s -XPOST http://127.0.0.1:9090/sandbox -H 'content-type: application/json' \
       -d '{"ports":[3000,3001],"auth_ports":[3001],"env":{"ANTHROPIC_API_KEY":"sk-ant-..."}}' \
       | jq -r .id)
TID=$(curl -s -XPOST http://127.0.0.1:9090/v1/sandboxes/$ID/tasks -H 'content-type: application/json' \
       -d '{"prompt":"build a Vite todo app on port 3000","agent":"opencode"}' | jq -r .id)
curl -N http://127.0.0.1:9090/v1/sandboxes/$ID/tasks/$TID/events

# 访问预览
open "http://s-$ID-3000.preview.localhost"   # 用户的 dev server
open "http://s-$ID-3001.preview.localhost"   # agents-ui 控制面板(走 forward-auth)

# 看一眼
docker ps --filter label=sandboxd.managed=true
docker compose logs --tail=50 sandboxd

# 收摊
./uninstall.sh --all -y
```
