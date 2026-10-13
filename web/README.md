# 叮当words · 前端

React 18 + Vite + TypeScript + Ant Design 5 + Zustand。设计规范见 [docs/07-UX设计规范.md](../docs/07-UX设计规范.md)。

## 开发

```bash
npm install
npm run dev        # http://localhost:5173（代理 /api 与 /ws 到 127.0.0.1:8080）
npm run build      # 产物 dist/
```

需先启动后端：`cd ../server && go run ./cmd/server`（首启自动播种 7 库 5568 词 + 引导创建管理员）。

## 结构

```
src/
├── api/client.ts      # REST 客户端（{code,msg,data} 约定 + token）
├── stores/auth.ts     # Zustand 登录态
├── pages/             # 登录 / 词库广场 / 摸底 / 课程表 / 课堂 / 统计
└── App.tsx            # 路由（未登录重定向 /login）
e2e/smoke.spec.ts      # Playwright 无头冒烟（docs/08）
```
