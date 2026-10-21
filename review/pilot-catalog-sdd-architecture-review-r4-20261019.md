# Catalog 架构复审 r4

**PASS，0 阻断。r3 两项 P2 均已闭合。**

审阅 docs41 SHA-256：`01d37dafdabd41715a643afe5703c8ed4b18e0a498c78309a70b71758817309c`。

- **CAT-R3-01**：第128、130行明确所有监督器 startup 先取得同一持久、已验证的 `catalog.bootstrap.lock`，覆盖初建及完整首帧、文件和父目录同步。其他 startup 不再能把进行中的空 catalog 当损坏。第168行将 bootstrap 锁与 case 产物明确区分。
- **CAT-R3-02**：第152、174行明确每次 startup 都重新同步 catalog 及父目录，成功后才授权 case。初次 parent Sync 失败后的完整帧恢复不再跳过耐久性屏障。

锁序一致：bootstrap 可等待 catalog，但取得 case 锁前必须释放；普通 case 操作不获取 bootstrap，继续采用 case→catalog 顺序。provisioning 恢复仅检查固定初始成员，不能启动课堂或调用 Planner/Registry；完整初始状态才能转 ready，否则隔离。并发恢复须使用同一稳定 case 锁身份。

实现验收仍须锁定 CREATE_NEW 后暂停时另一 startup 等待、创建者死亡后的空/残帧失败，以及恢复重新 Sync 失败时零 case 产物。各平台 OS 锁、ACL、父目录同步和进程死亡释放尚需实际验证。

本轮仅定点设计复审，未运行测试、构建、服务或修改设计/产品。
