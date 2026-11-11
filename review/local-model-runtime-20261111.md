# 本机语音模型运行时排查（2026-11-11）

## 本轮结果

- 已恢复 `D:\VL-models` 的 8090 管理服务；`GET /api/status` 可返回服务状态。
- 中文 TTS 在 8002 就绪。经 8090 代理提交公开合成文本“这是一只猫。”，返回 HTTP 200、32,812 字节 WAV，用时 3.804 秒。此结果只证明合成链路可用，不代表首音延迟或真人课堂验收。
- VL 文本服务与 ASR 均未就绪。`llama-server.exe` 启动后曾立即退出；Windows Application Error 1000 记录 `MSVCP140.dll` 中 `0xc0000005`。更新微软签名的 x64 VC++ v14 运行库后，系统 DLL 从 14.29.30135.0 更新至 14.52.37029.0；安装器退出码 3010（需要重启）。更新后 ASR 进程仍停留在 `starting`、8001 未监听，已用管理接口停止，避免留下占用资源的悬挂进程。

## 后续验收门

在可安排的系统重启后，依次运行平台的 `start-model-console.ps1`、`start-qwen-vl.ps1`、`start-qwen-asr.ps1` 和各自的 `status-*.ps1`；先确认 8000/8001 监听及健康请求，再运行公开合成音频的 ASR 对照、`-providers local` 启动和浏览器课堂旅程。若重启后仍崩溃，读取新的 Application Error 与服务日志定位，不将服务清单中的“有能力”误认为实际可用。

当前已通过的 Mock 老师及文字降级课堂旅程见 `review/business-closure-20261111.md`；真实语音和课堂试点仍未验收。
