# 浏览器 E2E 与开发镜像制品门禁实现计划

> **For AI Agent Workers:** 浏览器行为先写会失败的 Playwright 场景，再接入真实 Go Control Plane；容器扫描在没有 Docker 时只能标记静态配置，不能声称远端绿灯。

**目标：** 用可重复的 Chromium E2E 证明构建后的 SPA、真实同源 API、Cookie/CSRF、深链路与退出登录组合工作，并把同一开发镜像的启动烟雾、漏洞报告和 SBOM 纳入 CI。

**架构：** Playwright `webServer` 从仓库根启动单进程 development-memory Control Plane，由它直接托管 `web/dist`；测试必须观察真实 `/api/v1/*` 响应和固定种子事实，禁止浏览器 Mock 回退制造假绿。容器 job 只构建一次同一 commit 镜像，随后启动、扫描并生成报告；不推送、不读取 Secret。

**技术栈：** Playwright 1.62.1 / Chromium、React 构建制品、Go 1.26、GitHub Actions、Trivy 0.72.0、CycloneDX JSON。

## 任务 1：真实 Control Plane 浏览器 E2E

**文件：**

- Modify: `web/package.json`
- Modify: `web/package-lock.json`
- Create: `web/playwright.config.ts`
- Create: `web/e2e/control-plane.spec.ts`
- Modify: `.gitignore`

**完成标准：** `npm run e2e` 构建 SPA，启动隔离 Go Control Plane，Chromium 完成 ready → 深链路登录 → 固定服务器 → 退出；请求证据来自真实 API，任何 Mock 回退都失败。

- [x] 固定 `@playwright/test 1.62.1`，新增 `e2e`/`e2e:ui` 与构建前置脚本。
- [x] 配置单 worker、固定回环端口、禁止复用旧服务、隔离数据根、失败 trace/screenshot/video。
- [x] 先写登录/深链路/退出场景并确认后端未启动或 API 证据缺失时红灯。
- [x] 接入 `webServer`，本机安装 Chromium 后转绿；用一次 API 凭据不匹配 mutation 证明 Mock 不能假绿并恢复。

## 任务 2：E2E CI 与证据留档

**文件：**

- Modify: `.github/workflows/ci.yml`

**完成标准：** Ubuntu job 使用 Go 1.26、Node 24、`npm ci` 和 Playwright 官方 CLI 安装 Chromium；失败或重试证据保留 7 天，权限只有 `contents: read`。

- [x] 新增 15 分钟超时的 `browser-e2e` job，执行 `npx playwright install --with-deps chromium` 与 `npm run e2e`。
- [x] 非取消情况下上传 `web/playwright-report` 与 `web/test-results`；不使用 `pull_request_target` 或 Secret。
- [x] actionlint 静态验证工作流；只有真实 GitHub run 后才可声称 CI E2E 绿灯。

## 任务 3：开发镜像启动、扫描与 SBOM

**文件：**

- Modify: `deploy/Dockerfile`
- Modify: `.github/workflows/ci.yml`

**完成标准：** 同一 `github.sha` 本地镜像通过 `/readyz` 与 SPA 烟雾，对同一镜像生成 CycloneDX 和完整漏洞 JSON，再对有修复版本的 HIGH/CRITICAL 漏洞阻断。

- [x] 把 Web build 基础镜像从 EOL Node 25 对齐到受支持的 Node 24 LTS；记录其余浮动基础镜像 digest 仍待维护机器人治理。
- [x] 容器 job 构建一次、回环启动、轮询 ready 与首页，失败输出日志且总是清理测试容器。
- [x] 固定已知安全的 Trivy Action commit 与 Trivy 0.72.0，生成 CycloneDX/JSON，上传报告并执行 HIGH/CRITICAL 门禁。
- [ ] 在可用 Docker 的 CI 或远端真实运行；本机无 Docker 时不得勾选真实制品结果。

## 任务 4：文档与实现记录

**文件：**

- Modify: `README.md`
- Modify: `docs/development/LOCAL_DEVELOPMENT.md`
- Modify: `docs/design/11-testing-roadmap.md`
- Modify: `docs/changes/README.md`
- Create: `docs/changes/GM-20260808-011.md`

**完成标准：** 区分本机真实 Chromium、CI 静态配置、尚未执行的容器扫描；不把 development-memory 镜像写成生产发布。

- [x] 记录本地 E2E 命令、公开测试凭据、隔离数据目录和失败证据路径。
- [x] 更新当前 CI/阶段 0 状态，保留跨浏览器、Docker Agent、真实 Runtime、生产 Store 和 release attestation 缺口。
- [x] 追加 GM 记录并运行前后端、E2E、actionlint 与 Markdown 链接回归。
