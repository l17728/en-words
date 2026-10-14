# 中文TTS候选补丁、验证与应用说明

2026-10-14。**候选完成隔离验证后，已于16:40授权应用并通过部署烟测；随后原冻结12条中文TTS自动及人工试听全部通过。** 未重启8090、8001、文本/视觉模型，未更换任何权重或修改系统代码页。完整浏览器媒体旅程仍单独验收；以下隔离阶段pending保留为历史记录。

## 可评审产物

- `platform-tts-utf8.patch`：针对当前平台`manager/tts-server.js`与新增`manager/tts-utf8-worker.cpp`的统一补丁。
- `platform-tts-server.candidate.cjs`：自包含候选wrapper，测试不依赖ignored文件。
- `platform-tts-utf8-worker.cpp`：同发行包C API helper，Windows宽字符文件路径；正文通过UTF8文件传递。
- `platform-tts-wrapper.test.cjs`：7条隔离生命周期测试，mock进程，不宣称真实平台HTTP已回归。
- `build-platform-tts-patch.py`：从平台原wrapper生成以上补丁，**只读平台并写本仓库review**；平台若已更新，先复核diff，不能覆盖原始基线证据。
- `tts-utf8-artifact-hashes-20261014.json`：原wrapper、头文件、C API/ONNX DLL、模型、候选源码和已编译helper SHA256。

## 根因与等价性

有效原请求“猫”得到500，OOV字符为`è`。系统ACP936将“猫”编码为`C3 A8`，而UTF8按该字节解码恰为U+00E8。原wrapper经PowerShell将正文作为CLI位置参数；官方CLI使用窄argv。改用同模型C API读取`E7 8C AB`后，“猫”生成合法非静音WAV，确认故障在中文正文的窄argv编码边界，不是模型权重损坏或产品超时。

实际CLI `--help`核对了默认参数：noise_scale=0.667、noise_scale_w=0.8、length_scale=1、silence_scale=0.2、max_num_sentences=1；helper保持这些值及CPU/4线程、voice、speed、model/tokens/lexicon/三项FST。dict_dir仍传入相同目录（当前CLI说明此字段已不使用）；未猜测`--text-file`选项。

helper仅从可执行文件所在平台根解析可信模型/DLL，LoadLibraryExW指定DLL目录加载；输入路径由wmain/文件系统宽字符接口读取，输出CreateFileW创建，避免把正文编码问题转为用户名路径问题。wrapper以独有临时目录写UTF8，直接执行helper，取消杀实际进程；回调后清理成功/失败/超时/取消临时文件。HTTP接口、单并发限制及平台120秒上限不变；产品20秒上限不延长。客户端错误不暴露原生stderr中的正文/路径。

## 实际验证

| 路径 | 结果与证据 |
|---|---|
| 原平台“猫” | HTTP500，7.890秒；`tts-cat-valid-diagnostic-20261014.json` |
| 同模型UTF8 C API小矩阵 | 猫8.046秒、苹果5.735秒、猫，苹果。7.062秒均合法非静音；`tts-utf8-isolated-20261014.json` |
| 中英混合 | cat是猫。5.875秒生成WAV，但stderr明确忽略OOV `cat`，**有内容缺失，不能算通过** |
| 实际候选wrapper+实际候选helper | 独立18002 HTTP“猫”200，7.000秒，15916字节、PCM16/mono/16kHz、RMS431.4；`tts-utf8-candidate-http-20261014.json` |
| 生命周期单测 | 7/7：UTF8文件、成功清理、同步spawn异常、子进程错误、超时、取消/已取消、独立请求路径；部分测试族合并统计 |

隔离进程PID17388已退出；隔离目录`.test-tools/tts-utf8-platform`的models/sherpa-onnx为指向既有资源的junction，仅读取。**不要对该目录递归删除或执行跨链接清理。** 所有声音人工试听仍pending。此前12条中10个合法WAV也可能包含乱码发音，不能把旧容器成功率当内容正确率；应用后应按原冻结12标签重跑，不改分母。

## 构建与应用前核验

本轮编译使用已安装LLVM MinGW，未引入产品运行时Python依赖：

```powershell
& '.test-tools\llvm-mingw-20261006-ucrt-x86_64\bin\clang++.exe' -std=c++17 -O2 -Wall -Wextra -Werror -static -municode '-ID:/VL-models/sherpa-onnx/include' 'review/platform-tts-utf8-worker.cpp' -o '.test-tools/tts-utf8-worker.exe'
node --check review/platform-tts-server.candidate.cjs
node --test review/platform-tts-wrapper.test.cjs
```

由后台测试代理执行验证。部署前重核SHA表：原wrapper若不再是`6fea95d5fe54546658a569944991a1584dd5e5c3927cf9d77b66512a869829c7`则停止，不能覆盖他人更新。候选helper编译结果SHA以本次表为准；重新编译可能改变二进制hash，需重新记录而非盲套旧值。头文件/DLL与当前模型也须一致。

## 批准后仅8002应用步骤（本轮已执行，留作审计）

1. 确认没有真实媒体请求。读取`D:/VL-models/chinese-tts.pid`，用Win32_Process核对该PID的ExecutablePath为Node、CommandLine精确指向`D:/VL-models/manager/tts-server.js`，并核对8002监听PID一致。当前调查值12616仅作历史，不能硬编码用于停止。检查没有该服务的活动合成子进程；有则等待其终止。
2. 在`D:/VL-models/service-backups/tts-utf8-<唯一时间>/`备份原wrapper、原PID与SHA；若目标helper源码/二进制已存在，也备份并记录，不覆盖未知文件。所有最终路径必须在上述明确平台根内，不跨shell拼接删除命令。
3. 只停止已验证的8002 PID。现有stop脚本只按PID停止、没有核验命令行，因此不要未经上述校验直接调用。保存当前8002 PID文件后，用LiteralPath覆盖三个受控manager文件：wrapper、helper源码、helper二进制；不改models、DLL、manager/server.js或配置/密钥。
4. 以`Start-Process -WindowStyle Hidden`启动Node及该wrapper，WorkingDirectory为平台根、仅该子进程`TTS_PORT=8002`；stdout/stderr到新诊断日志，写回新PID。立即核对新PID与8002监听，确认8090/8001原PID仍在。
5. 保持原20秒客户端预算，经8090做“猫”烟测，再由后台代理跑真实完整媒体E2E与原12条TTS矩阵；还需取消、失败、忙碌响应及临时文件残留检查。音质与内容仍需人工，中文模型忽略英语不能靠编码修复宣称解决。

## 回退

若健康检查/烟测失败，停止经命令行和监听双重核验的新8002 PID，恢复备份wrapper及原有helper文件，按同样Hidden启动方式重启旧wrapper并核验8002。若原无helper，可保留未被旧wrapper引用的候选文件供诊断，或仅在SHA匹配本次新增文件时逐一LiteralPath删除；不得递归删除模型或junction。回退恢复的是原已知中文编码故障版本，不能因此宣称功能已通过。记录新旧PID、备份路径、文件SHA与结果，不记录密钥或用户正文。

## 实际部署结果

8项SHA门通过，原wrapper备份于 `D:/VL-models/service-backups/tts-utf8-20261014-164025`；旧8002 PID12616仅有conhost子进程，无合成任务。仅该服务Hidden重启为8908，8090=3552、8001=7728保持不变。经8090“猫”请求HTTP200/3093毫秒/14892字节，16kHz单声道PCM16、7424采样、RMS411.97；未回退。完整记录 `tts-utf8-deployment-20261014.json` 与 `tts-utf8-deployed-smoke-20261014.json`。执行脚本 `apply-platform-tts-utf8.ps1` 拒绝覆盖既有部署证据，烟测脚本使用Unicode转义避免PowerShell管道损坏中文。人工试听pending，混合英文缺失不是本次编码修复的成功项。

后续原12条复测与逐音频SHA人工确认已记录于 `local-tts-quality-20261014-utf8-fixed.json` 和 `tts-human-acceptance-20261014.json`：12/12自动通过，用户确认全部清楚正确完整。上文pending仅指部署烟测当时；此签收不扩大到混合英文或完整课堂旅程。
