# 中转机连接文档重新读取结论

按用户本次授权，经已知主机验证的现有 SSH 第一跳只读重新读取 `/root/llm-api-connection.md`；凭据只在内存解析，没有尝试第二跳登录，没有模型请求、服务变更或用户音频访问。

文件为 2199 bytes，mtime 为 `2026-10-15T07:12:53.097332+00:00`，SHA-256 为 `0af7650c2c40bb35e9817ec4fd38a88b57367f28f740f801cd32d7cb3b3d6c13`。mtime 只是文件时间，不作为可靠的部署更新时间。

可提取的契约仍是中转地址 `http://124.156.193.122:8001`，OpenAI 风格 `/v1/chat/completions`、`/v1/models`，以及 `/v1/messages` 示例。文档出现 `qwen-omni`、`minicpm-o`、`claude-sonnet-4-5` 名称；这些只能证明文档的模型别名或请求示例，不能证明实际加载了三个不同模型及其 revision。

本次关键词与参数提取只找到 `max_tokens`，没有 `enable_thinking`、`thinking`、`reasoning_effort`、`input_audio`、`modalities`、ASR 任务/语言、实时分片、取消 ACK 等实现契约。未发现明确指向其他源码/文档的 `/root/...` 路径。因此本文件不能解决上游音频角色、思考开关、模板或原生双工协议的缺口。

与 `review/remote-gateway-contract-index-20261015.json` 比较，原文档当时已记录 input_audio/audio_url/采样率/实时/cancel 等关键词缺失；本次结果与这些缺口一致。旧证据没有记录该文档 SHA，不能声称做过逐字节无变化验证。本次两次只读提取（第二次补充根路由及模型名 token）得到相同 SHA，原摘要均独立保留。

原始文档、授权头或密码没有写入报告。安全摘要见 `remote-connection-document-refresh-20261016.json` 与 `remote-connection-document-refresh-v2-20261016.json`。前次 nginx 与单次 OpenAPI 证据仍有效：中转至独立 `http://1.92.241.129:8001`，OpenAPI 返回 404。此处没有追加请求以猜测参数，也没有将文档示例或 nginx 版本冒充上游模型部署版本。

下一步仍需用户提供实际上游服务的配置/模板/版本契约或明确授权的相关文档位置；当前不以调整 prompt 或更换角色再次尝试取绿。
