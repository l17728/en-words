# 平台 TTS UTF-8 候选独立评审

日期：2026-10-14。评审范围：`platform-tts-utf8.patch`、`platform-tts-utf8-worker.cpp`、`platform-tts-server.candidate.cjs`、`platform-tts-wrapper.test.cjs`。仅仓库内候选审查，没有修改或重启共享平台。

## 结论

静态审查通过，允许继续隔离实际 HTTP 验收；不等同于共享服务已修复，也不等同于真人试听或语音质量通过。

- 中文正文写独有临时目录中的 UTF-8 文件，原生辅助程序按字节读取并验证有效 UTF-8，避免经过 Windows 窄字符命令行。正文不再放在 argv。
- VITS 模型、tokens、lexicon、dict_dir 和三个规则 FST 与原平台参数保持一致，CPU 4线程；保留原 speed 和 speaker 输入范围，拒绝空值、非有限速度及超限文本。
- 每次请求使用独有工作目录及排他输出文件。辅助程序直接由 execFile 启动，取消杀直接子进程；等待子进程回调后清理输入与输出。同步启动失败、进程失败、超时、预先取消均有清理路径。
- 保留原120秒平台超时，不通过放宽时限掩盖故障。原生stderr可能含正文，候选仅返回有界退出码错误。
- worker输出合法单声道PCM16 WAV，采样有限性检查、幅度限制、长度与字节率明确；模型资源及音频资源完成后释放。

## 发现与闭环

首次审查发现隔离测试依赖被忽略的 `.test-tools` 候选文件。已改为读取同目录已提交候选源；`build-platform-tts-patch.py` 保存再生成方式。该生成器需要本机原平台源码，独立运行wrapper单测不需要该路径。

主代理先前指出的 dict_dir 遗漏、同步execFile异常清理、空speaker校验均已在本次复核代码中修正。

## 独立执行证据

执行 `node --test review/platform-tts-wrapper.test.cjs`：7通过、0失败、0跳过，899.77ms。记录：`.test-artifacts/platform-wrapper-independent-final.txt`。

覆盖 UTF-8 中文字节、argv不含正文、成功清理、同步启动失败、子进程失败、超时、取消终止与等待回调清理、预先取消零启动、并发临时路径隔离。`git diff --check` 通过。

这些测试替换子进程执行器，只证明包装层生命周期和编码输入协议，不能证明模型DLL可加载、真实音频可懂或真实HTTP请求成功。实际原生构建、同模型合成矩阵、隔离HTTP及共享平台应用属于另列验收，不能借用这7条测试记作真实成功。

另独立执行已构建worker的6项非法输入：空speaker、NaN速度、空速度均退出2；超过8000字节、非法UTF-8、NUL正文均退出3，全部没有输出音频文件。证据 `.test-artifacts/platform-worker-independent-invalid.json`，6/6符合预期。这些路径均在加载模型前返回，未增加真实模型推理。

架构代理另提供实际隔离HTTP成功报告 `tts-utf8-candidate-http-20261014.json`；该证据与本文件的独立包装层测试分账。共享8002服务尚未因本评审应用补丁。
