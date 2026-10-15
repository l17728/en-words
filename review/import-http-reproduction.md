# D 盘 HTTP 基线复现

入口保存在 `review/measure_import_http.py`，随报告版本化，不依赖被忽略的 `.test-tools` 脚本。原测量脚本 SHA256 为 `9fcc48377e8036def596ba7067eb6b1e536e702c19fdaa5c74c2225b3fbce6ef`，与 `import-enable-http-comparison-20261015.json` 一致。

在仓库根目录显式运行（先准备对应服务器二进制）：

```powershell
python review/measure_import_http.py --binary .test-artifacts/interaction-placement-server-20261015.exe --run import-http-reproduce-unique-name
```

`--run` 必须是 `.test-artifacts` 的直接子目录且尚不存在，避免覆盖旧证据。脚本以自己的服务器进程、全新 `data.db`、端口 18183、`-testmode -providers mock` 执行；端口占用即拒绝。只请求本机该端口，不读密钥，不调用模型或共享服务，不改全局 SQLite 配置。测试账号密码是隔离数据库的公开夹具，token 不写报告。标准输出直接落独占日志文件，退出仅终止该脚本启动的服务器 PID。

测量含 100/1000 条导入、发布、首次启用、重复启用，以及另一学生读取词库的业务正确性和延迟。报告记录二进制 SHA、各操作耗时及进程清理结果。单请求最多 150 秒、整轮 240 秒；预算中止只表示右删失观察，不等于产品主动返回超时。此命令会实际运行隔离测量，不应与浏览器或 race 验收并发。本次交付核查未重新执行测量。

新构建的二进制不等同历史旧/新二进制。复核历史性能数值应使用报告中记录的准确 SHA；缺失旧产物时，不可把当前版本结果标作旧基线。
