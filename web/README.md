# 叮当words · 前端

React 19 + Vite 8 + TypeScript 6 + Ant Design 6 + Zustand 5。用户已确认保留当前版本，锁定版本及ADR见 [版本基线](../docs/16-工具能力与版本基线闭环规格.md)，界面规范见 [UX设计](../docs/07-UX设计规范.md)。

## 开发

```bash
npm ci             # 按 package-lock.json 安装
npm run dev        # http://localhost:5173（代理 /api 与 /ws 到 127.0.0.1:8080）
npm run build      # 产物 dist/
npm run lint
```

需先启动后端：`cd ../server && go run ./cmd/server`（首启自动播种 7 库 5568 词 + 引导创建管理员）。

保存学习进度并使用本机8090模型平台时，后端使用`go run ./cmd/server -persist -providers local`。真实模型质量与媒体格式验收见docs15/21；`mock`默认模式只用于开发和确定性回归。

## 回归

在本目录运行`node --test tests/audio-wav.test.ts tests/ws-client.test.ts`（本机Node25支持直接执行可擦除TypeScript）验证PCM转换及连接恢复/终止状态。Playwright一律无头且独立数据库，禁止复用开发服务；例如`npx playwright test --config=playwright.closure.config.ts`。

真实进程重启套件另需先构建后端并设置绝对路径`E2E_SERVER_BINARY`；活课堂逾期套件还需测试harness二进制`E2E_HARNESS_BINARY`。全部套件、环境变量和用例映射见 [E2E迁移与恢复验收设计](../docs/17-E2E迁移与恢复验收设计.md)。确定性媒体mock与真实8090媒体分别验收，不共享准确率或通过结论。

## 结构

```
src/
├── api/client.ts      # REST 客户端（{code,msg,data} 约定 + token）
├── stores/auth.ts     # Zustand 登录态
├── pages/             # 登录 / 词库广场 / 摸底 / 课程表 / 课堂 / 统计
└── App.tsx            # 路由（未登录重定向 /login）
e2e/smoke.spec.ts      # Playwright 无头冒烟（docs/08）
```

## 完整隔离验收

在仓库根目录使用 `python review/run_browser_acceptance.py --server <后端绝对路径> --harness <Go测试harness绝对路径>`；8套无头Playwright配置覆盖73条，另跑Node11与lint，配置各自执行TypeScript/Vite构建。完整构建命令、端口/DB隔离和真实媒体显式`--media`规则见 [docs17](../docs/17-E2E迁移与恢复验收设计.md)。所有输出保存在新建`.test-artifacts/browser-*`，失败不覆盖历史记录。


当前完整验收入口（75 E2E / Node17）：先按docs17构建server与测试harness，再从仓库根执行`python review/run_browser_acceptance.py --server <absolute-server.exe> --harness <absolute-harness.exe> --output <new-report-directory>`。默认只跑隔离确定性后端；真实平台另显式`--media`并指定已人工确认的`--chinese-audio`，严禁把旧未确认音源当模型质量基准。


当前冻结树验收：78 E2E + 17 Node 用例、lint 0 警告、8 套构建通过。证据见 `../review/runtime-browser-acceptance-20261014-final78.json`；真实模型媒体另轨，不纳入78。
