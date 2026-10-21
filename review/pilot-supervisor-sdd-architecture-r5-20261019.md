# Pilot Supervisor SDD 架构复审 r5

**BLOCKED：1 项 P2 close-state 表述不一致。** R4 其它通过项沿用。

审阅 `docs/42-Pilot隔离运行监督器SDD.md`，SHA-256：`EB4CDE3000D7C66AE0A39D060970C180046335120DAAA295682C5B9AB0F25A01`。本轮只回归 FI-CLOSE 与 API/§9 的 close-state、restart-required 协议；R4 的 catalog/state/cleanup 结论复用。只读审阅，未改文件、未运行测试。

**R4 项目复核通过。** FI-DB-01a/b/c 现区分 `db_open_rw=0` 与最多一次受限 `mode=ro` recovery PRAGMA，关闭 R3-01。FI-CLEAN-01 覆盖所有合法来源状态及零字节/完整/partial frame；cleanup case directory 缺失时 pinned parent 复核并 Sync 后写 tombstone，关闭 R3-02。均与 docs/41 §109.2–109.3 一致。catalog lock、双阶段锁序和状态能力未见回归。

**SUP-SDD-R5-01（P2，§30、§93、§95 与 FI-CLOSE-01a/b/d/g）—still_open 重试结果与 FI 行的锁持有期限冲突。** API/§9 规定 `still_open` 由原 owner 在新 context 下重试同一个 handle；若重试最终确认关闭，关闭序列可以继续，最终释放 case lock（或 Supervisor 结束时释放 shared locks）。但 FI-CLOSE-01a/b/d 的错误结果列同时要求 `still_open` 可重试，结果列又无条件写“case lock 保持到进程退出”。这会让测试在成功重试后仍不允许释放锁，或迫使实现额外退出进程，与 API 的可恢复 close-pending 路径不一致。unknown 分支要求专属 child 有界退出/reap 并持锁到进程退出，则与 §32/§95 一致；问题只在 still_open 分支。

**要求：** FI-CLOSE-01a/b/d/g 将结果拆清：成功或 `still_open` 重试成功后继续关闭并按正常顺序释放 case/shared lock；`still_open` 重试未完成/调用方 context 超时则保留该 handle 与相关锁、返回 `close_pending`，仅原 owner 可续试；只有 `unknown` 才设置 `restart_required`、禁止再 Close/Unlock 并要求专属 child 在有界期限内退出和 reap。对应 restart/副作用断言分别列出。

除此之外未发现 R4 的其他项回归。修正此歧义后，建议复审 FI-CLOSE 表并保持 R4 PASS 结论。
