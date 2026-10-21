# Pilot catalog journal：实现切片后端复审

评审对象：`server/internal/store/pilot_start_catalog.go` 与 `_test.go`，对照 docs/41 §109。只读；未运行测试或修改代码。

## 结论

**BLOCKED：存在 1 项 P1 fail-closed 实现缺口，另有 P2 验收覆盖缺口。** Frame 编解码、CRC、规范 JSON、generation 连续性、startup replay 和正常状态迁移主路径实现清楚；但 `Transition` 达到 frame/byte 上限时仅返回 `errPilotCatalogLimit`，没有 poison journal，调用者仍可继续执行其它迁移/快照。这不满足 §109 对超过全局限额即全局 fail-closed 的约定。测试也未覆盖 Transition Sync 故障及真实跨进程 generation 竞争。

## 发现

| ID / 级别 | 位置 | 发现与建议 |
|---|---|---|
| CAT-CODE-1 / P1 | `pilot_start_catalog.go` `Transition` 的 max-frame 与 max-byte guard（约 173、190 行） | `len(records) >= pilotCatalogMaxFrames` 与 `info.Size()+len(frame) > pilotCatalogMaxBytes` 都直接返回 `errPilotCatalogLimit`，但不设 `journal.poisoned`。违反 §109 的“超过 catalog 上限为 global integrity failure”合同：该实例后续 Snapshot/别的 case Transition 仍可运行。应在返回前 poison catalog/supervisor，并锁定后续操作统一拒绝。 |
| CAT-CODE-2 / P2 | `pilot_start_catalog_test.go` `TestPilotCatalogWriteAndUnlockFailuresPoisonJournal` 及 Sync 故障测试 | Transition 的短写与 unlock 失败有测试；file Sync 错误只覆盖 first-frame create/startup replay（约 419–457 行），没有覆盖普通 Transition 在完整 frame 写后 Sync 失败时是否 poison、拒绝同实例重试。补该故障用例，并断言 frame 可能留存时重启只按完整链 oracle 恢复。 |
| CAT-CODE-3 / P2 | 并发及 canonical JSON 测试 | generation 并发测试使用 `catalogTestLock` 的同进程 mutex；平台锁测试仅同一个 handle 顺序 Lock/Unlock，未验证两个独立进程/独立 handle 争用 OS lock。也未见 JSON payload 层针对 whitespace、重复/未知键、尾随值、字段顺序、null、generation 前导零等 canonical 负例（frame 位翻转不等同于这些 parser 语义）。§109.4 明确要求 child-process generation 与 canonical 拒绝验收，应由 supervisor/子进程层补齐。 |

## 已核对通过项

- 编码是固定字段顺序 `json.Marshal`；解码先校验 frame magic、边界、CRC-32C、重复长度和 trailer，再 strict decode、重新编码逐字节比较。
- replay 验证全局 generation 从 1 连续递增、每个 case 的状态迁移合法、case ID 永不复用；frame/byte cap 在 replay 入口存在。
- `openPilotCatalogFromVerifiedHandle` 在全局锁下 replay、file Sync、parent Sync，最后 unlock；错误返回 integrity 且不发可用 journal。
- 首帧创建在锁下要求空 regular file，写 `generation=1/provisioning`，file Sync 后 parent Sync；短写、Sync 失败都会返回错误。
- Transition 正常路径在读完整链、验证 transition 后 seek-end、单次 append、检查短写、Sync；写/Sync/seek/stat/unlock 失败会 poison。非法业务迁移不写 bytes/generation。
- 现有测试包含 golden frame/CRC、逐字节截断与若干 framing 位损坏、状态重放/非法 generation/ID 重用、exact frame-count limit、startup Sync 顺序、短写/unlock poison 和两 journal 共享 fake lock 的并发 generation。

## 验收边界

当前切片依赖调用方提供已验证 file handle、OS lock 与 parent Sync callback；它不实现路径/ACL/file identity 或 catalog.bootstrap.lock。这与 §109 的 supervisor 分层一致，但跨进程 lock/identity/platform gates 不能由本测试中的 fake lock 通过来宣称完成。
