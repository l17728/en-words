# Catalog 架构终审 r5

**PASS，0 阻断。** 审阅 docs41 §109，源 SHA-256：`402d57e870b36daa3e1472c6d4351c33152b74614fb94999237b3af53087b967`。

1. startup 按 bootstrap→catalog 获取锁；完整验链、身份校验、文件及父目录同步持续持有 catalog 锁；先释放 catalog、再释放 bootstrap，之后才可获取 case 锁。普通 case 操作不反向获取 bootstrap，无已规定的锁序环。
2. 首次 CREATE_NEW 与首条 provisioning frame 构成同一初始化过程，持久化完成前不创建 case 锁或成员。catalog 丢失却有 case 产物时拒绝重新初始化；独立 bootstrap 锁不被误算作 case 产物。
3. provisioning 恢复只检查固定初始成员，不执行课堂或 Agent；并发恢复使用同一稳定锁身份并重查状态，最多追加一次 ready。
4. 一个 case 仅一个目标 Start；同 case 串行且锁内二次授权，不同 lesson 使用不同 case。未知结果不得伪装为新尝试。
5. case 锁在独立受保护根中保持身份；cleanup-resume 不打开 SQLite、sidecar 或 key，删除后同步父目录再记永久 tombstone。已删除目录的恢复仍遵守该同步屏障。

此前生命周期、初建互斥及恢复同步问题保持闭合。完整有效帧只证明恢复状态，不证明 HTTP 响应送达；CRC 不代替权限保护。

此为设计门通过，未运行测试或构建，未修改产品/设计。平台锁、ACL、文件身份、父目录耐久性及命名屏障故障用例仍须实现后实际验收。
