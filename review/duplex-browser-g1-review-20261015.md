# G1 浏览器与端侧验收记录（2026-10-15，尚未通过总验收）

## 范围与冻结身份

仅端侧预检、实际设备设置三态、PCM/本地VAD/资源生命周期；不上传音频、不识别、不判分、不开放原生双工或自动教学。现有课堂生产源保持原语义。

- 产品组合SHA256：`47f351de9eee004724b21788fad273a8f651c685ed5c942ec03887d8bfb96939`，本轮前后不变：`True`。
- server：`b73c9a80719f6a7cec401d39d93e7a2c9f44ea0f552114c0f81d76195c50465c`。
- harness：`5eb847daa82382f372226de88f287c1e7828a02e400c3823a3d63b492af5801d`。
- AGENTS未修改；未提交、未推送、未修改共享模型服务。

## 已通过的证据

- Node最终74条通过；lint 0警告/0错误，正式TypeScript/Vite构建通过。见 `.test-artifacts/duplex-final124-r4-20261015/node-unit.log`、`lint.log`、`recovery-shard1.log`。
- 完整预检专项7/7，见 `.test-artifacts/duplex-complete7-20261015/manifest.json` 与 `duplex-results.json`。首轮6/7失败是HTTP200+业务code契约误断，原件保留，不能把6+单例当整族。
- REC13真实owned后端信号退出后再次stop/restart、同课恢复并首答100%定向1/1，见 `.test-artifacts/owned-process-rec13-results.json`。
- 端侧真实泄漏边界Node红绿、进程signal终态/cleanup短路两红、迟到stdout write-after-end一红均保留；修后owned-process四条绿。
- runner完整性与清理6条Python单测通过：42个discovery ID冻结；两21组必须无重复且并集精确42；每ID一次passed/retry0，skip/flaky/遗漏均拒绝；超时清理自有进程树。

## 总验收未通过与性能边界

最终计划124=恢复42+逾期1+反馈26+历史9+journey1+closure6+firstuse1+playback1+interaction30+预检7。**目前并未完成124。**

最新r4第一固定21组：19通过、2失败、0skip/flaky，13.5分钟；后续组因fail-stop未运行。完整清单、每ID与源/二进制身份见 `.test-artifacts/duplex-final124-r4-20261015/manifest.json` 和 `recovery-shard1-results.json`。

1. terminal403场景尚未进入用户步骤，独立新库迁移在原30秒启动门内未就绪。路径 `web/.test-artifacts/closure-1792056763503-8576/browser/recovery-regression-termin-65df4-able-error-no-endless-retry`。
2. REC04多学生旅程在导航后原15秒期待跟读，实际在线且仍在真实声明的朗读序列。路径 `web/.test-artifacts/closure-1792056925753-19200/browser/recovery-regression-REC04--c2d2a-ile-one-network-disconnects`。随后经架构复核改为A/B真实enter各30秒、B四段completed各原15秒，再保留断网/隔离/恢复断言。定向`rec04-phase-results.json`1/1通过（用例1.4分钟）；整21未重跑，不能据此标该族通过。

此前G09一次enter22.598秒，其中classroom.say finalize22.594秒，lock/begin/config/audit均0；JD28另次enter7.177秒后4段按默认1500毫秒间隔完成。日志只锁定COMMIT/driver/fsync/调度所在收尾阶段，不能进一步武断归因磁盘或模型。**入课长尾是交互性能未通过事项**，G09/JD28经专家允许按真实enter/首play_tts/逐段completed再启动原媒体15秒，不等于产品已经提速。原30秒请求、15秒断言、每分组900秒未提高。

## 失败与主动中止历史

- `duplex-final123-20261015`：首轮恢复族未完，900秒超时，含退出15秒与迁移30秒失败。
- `duplex-final124-20261015`：主审发现cleanup迟到stdout风险，主动停止owned树，非产品失败。
- `duplex-final124-r2-20261015`：恢复36条中34过2失败，随后900秒截止；保留G09/JD28阶段证据。
- `duplex-final124-r3-20261015`：用例前因r2遗留owned Vite端口占用失败；已精确清理且修runner超时树清理，未启用reuseExistingServer。
- `duplex-final124-r4-20261015`：固定第一21整组19过2失败，工具正常fail-stop。没有以散落通过结果拼接全量通过。

## 下一步

先完成冷迁移/提交长尾归因及REC04阶段合同复核，再由主审决定功能/性能分账验收的后续执行；不继续盲重跑或扩超时。实际声学、回声抑制、多人真实全双工与Room统一提交仍是独立未过门事项。


补充：runner固定分组/终止树/G09/JD28同步已获架构实际复核通过。REC04定向期间已实际触发后台只读磁盘/owned PID采样，记录由后台提供；此前发送给idle代理的采样请求没有执行，因此没有此前30样本数据，不能追补伪造。全124仍未通过。
