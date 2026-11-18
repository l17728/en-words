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

先按 [docs17](../docs/17-E2E迁移与恢复验收设计.md) 构建server与测试harness，再在仓库根执行：

```powershell
python review/run_browser_acceptance.py --server <absolute-server.exe> --harness <absolute-harness.exe> --output <new-report-directory>
```

入口串行运行8套隔离无头配置，严格核验86 E2E、27 Node、lint及各套TypeScript/Vite构建；默认不访问共享模型。当前同生产树86条分批全部通过（首批77+修正旅程夹具后9），Node27、lint0警告；首批计时失败保留。证据见 `../review/runtime-browser-acceptance-20261014-final86.json`，不宣称单次全绿。

实际TTS字幕另轨1/1通过，见 `../review/real-subtitle-acceptance-20261014.json`；复现需绝对路径`E2E_SERVER_BINARY`与协调8090空闲窗口后运行 `npx playwright test --config=playwright.subtitle-real.config.ts`。该项仅英3段/中文完整句1段播放字幕，无ASR/判分。原B完整课堂真实模型失败仍单列；`--media`不得隐式开启或用mock结果代替真实质量。
