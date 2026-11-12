# 本地 ASR 失败闭环验收（2026-11-12）

## 结果

跟读和答题相位的识别失败均发送携带 `epoch/turn` 的 `asr_failed`。服务端暂停并持久化 `input_failed`，保留原题和作答机会；重复或迟到事件被拒绝。恢复时重置输入计时窗口。浏览器提供“重试语音”和“继续并使用文字”，文字模式按课程及逻辑题目保存在同一标签页，下一题自动清除。

## 缺陷与回归

Playwright 严格旅程发现浏览器 SpeechRecognition effect 的清理函数误中止本地 `recordLocal`，导致下一题显示“语音就绪”但不录音。拆分浏览器识别器清理与本地录音中止后，原失败旅程 `v9` 两例通过（48.7 秒）：跟读失败→刷新→文字继续→新答题相位只录一次→失败→文字继续→正确结课；答题失败→恢复接口首次 503→重试→文字提交，未重复判分。每个 turn 启动录音前设置捕获门，失败待确认期间不重录。

`go test -p 1 ./...` 全包通过，`go vet ./...` 通过，前端构建通过。覆盖率：API 82.3%、classroom 90.2%、localmodel 89.8%。用例位于 `server/internal/classroom/asr_failure_test.go`、`server/internal/api/asr_failure_ingress_test.go`、`web/e2e/interaction-boundaries.spec.ts`。
