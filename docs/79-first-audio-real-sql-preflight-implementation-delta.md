# 首音真实 SQLite PREFLIGHT 实现差异记录

## 范围

本记录跟踪 `docs/78-first-audio-real-sql-preflight-amendment.md` 中真实 SQL PREFLIGHT 的实现证据。当前正向集成切片覆盖三条同时 pin 的物理连接；原规格保持不变；本记录不扩大到 `JOURNEY`、浏览器验收或课堂验收。

## 已实现与验证

- 在隔离的 SQLite 文件上播种合成用户、词库和课程，安装固定启动拒绝触发器及三条课程探测触发器，并保存完整数据库快照。
- 通过真实 mattn SQLite/GORM 连接池启动 guarded `api.NewApp`。controller 顺序经过 `DRIVER_PROBE → STARTUP_PROBE`，启动事务完成后才固定唯一物理连接；启动后快照与准备快照一致。
- startup permit 仅包围同步 `NewApp` 调用，并要求底层 authorizer 恰好观察一次 BEGIN 和一次 COMMIT/ROLLBACK；startup permit 之外的事务元组拒绝。
- `PREFLIGHT` 中仅用一次性 permit 读取快照、开始事务、消费同连接挑战、读取 18 列类型化课程投影、执行固定 `id=id` 更新并验证恰好一次 OLD/NEW 回调，最后显式回滚并核对完整快照哈希和规范内容。
- PREFLIGHT context 显式固定逻辑或物理 probe 模式；receipt 注册时必须与该模式一致，clone/publish 会保留该标记，防止把未执行物理读回的逻辑 fixture 当作真实候选。
- tracked driver 为每次成功 `BeginTx` 分配递增 epoch；token/UDF 与 row-guard 回调必须匹配同一活动 epoch。专用 cleanup 方法持有唯一 `sql.Tx`，回滚后验证 autocommit 和 inactive；无法确认时通过 `driver.ErrBadConn` 驱逐物理连接。
- receipt 状态依次经过回滚确认和快照读回确认；`completeFor` 只接受读回终态。真实 SQL challenge 从创建时固定要求物理读回，只有 authorizer 的真实快照比较路径可完成它；该路径绑定前后快照摘要、challenge 事务 epoch、原物理连接及 registry 活性。纯状态机 fixture 使用分离的测试辅助路径，不能完成物理 challenge。typed REAL 比较按 IEEE-754 位模式处理，覆盖 `+0` 与 `-0`。
- 真实连接的读回终态 CAS 在 registry live lock 内完成；后续发布 JOURNEY 时，controller 会再次验证所有真实 readback 连接，并在发布上下文期间保持注册表锁，防止 retire 与发布之间竞态。
- schema manifest 读取保留 SQL NULL 的规范化语义；仅接受三个经专用 DDL 校验的 `fa_lessons_guard_*` 触发器，额外同命名空间触发器会被拒绝。
- 快照规范中的表列集合与批准的 schema 读取列清单做精确比较，锁定缺列、额外列和漏表。
- 作者化器拒绝元组由确定性用例覆盖；捕获器注释明确其总是放行、不得作为安全策略安装。

验证命令（Windows CGO）：

```powershell
go test ./internal/api -run '^TestFirstAudio' -count=1 -timeout=5m
go test ./internal/api -count=1 -timeout=10m
```

截至 2026-10-28，针对本功能的 `go test ./internal/api -run '^TestFirstAudio' -count=1 -timeout=5m` 通过。全包 API 回归及独立架构、安全评审须在本次最终改动后重新执行并记录；此前通过的回归不覆盖后续改动。

## 明确未验收

本正向切片不证明 §5 真实 SQL 负例隔离子进程、取消/超时与 drain 竞态、失败回滚/连接销毁注入、双进程 Playwright journey、跨复位 stale callback 隔离、性能矩阵或真人课堂效果。不得据此标记首音课堂矩阵通过。

## 三物理连接正向 PREFLIGHT 增量（2026-10-28）

`TestFirstAudioMatrixRealSQLProbeTransactionAndReceiptUnderExactPermits` 现在固定池容量为 3，并在启动前及 PREFLIGHT 前分别同时 pin 这三条连接。测试从每个 `Conn.Raw` 获取 tracked raw 指针，要求注册 ID 唯一且 hook 注册总数恰为 3；每个 ConnectHook 创建独立 authorizer、session、permit 和连接级 UDF。NewApp 的 startup permit 按连接实例分别开启并汇总断言恰好一组 BEGIN/结束事务。

PREFLIGHT context 一次发布三个 connection-bound challenge/contract/receipt。协调器逐连接消费各自 token、读取固定投影、执行唯一 no-op UPDATE、显式回滚并在同一 pinned raw 连接完整读回 snapshot。每个 receipt 必须到达物理 readback 终态；所有探测后重新核对全库 setup baseline，释放 pins 后再次确认相同 raw/ID 仍在 registry。TX_CLEANUP authorizer tuple 同时要求本地注册 ID/raw、活动 session、tracked 连接及事务 epoch 一致；token slice 在创建挑战前注册统一清理。

提交 `f1db42e` 的 CI：[Linux/Windows 控制协议检查](https://github.com/l17728/en-words/actions/runs/40635891277)。工作流还包括 CGO API 定向回归、Linux race、vet、Web build/typecheck 与生产符号检查。CI 通过只证明该工作流内检查通过，不代表 docs/78 §5 完整负例、JOURNEY、Playwright 双进程旅程或性能/课堂验收通过。三领域评审记录见 `review/first-audio-real-sql-preflight-multiconnection-review-20261028.md`。

## Review 与后续

实现评审完成后记录评审者、结论、问题及关闭证据。下一阶段继续依照 docs/78 的失败候选清单接通隔离候选 harness，再逐项完成数据库双进程旅程与性能矩阵；真人音频仍须等加密链路、服务端音频协议和课堂授权条件满足后单独验收。

## Latest cleanup review and verification (2026-10-28)

The rollback failure path now terminalizes the guard and discards the uncertain pinned connection. Retirement is accepted only when the connection registry no longer recognizes the raw identity and the tracked driver confirms the underlying `Close()` returned successfully. A regression test exercises this terminalize-and-discard path. The phase predicate test uses direct immutable-context publication because it tests only phase authorization; lifecycle transitions remain covered by the separate controller tests.

Final security re-review found no remaining P1/P2 in this slice. See `review/first-audio-real-sql-preflight-multiconnection-review-20261028.md` for the architecture, security, test/CI findings, fixes, and remaining acceptance scope.

Commit `3bf4a5f` CI passed both Linux and Windows jobs: [First-audio control protocol checks](https://github.com/l17728/en-words/actions/runs/40637306873). The local API test passed with CGO disabled; the CI jobs provide CGO evidence. The previous `16cc255` and `f91ee0a` CI failures are superseded by the corrected run and are not acceptance evidence.

This verifies only the positive real-SQL PREFLIGHT slice and its cleanup fix. The remaining docs/78 §5 fresh-candidate negative matrix, isolated-backend harness integration, guarded `api.App` journey, dual-process Playwright journey, reset races, performance matrix, and real classroom pilot remain open.

The same `3bf4a5f` also passed the [Pilot Linux package and manifest workflow](https://github.com/l17728/en-words/actions/runs/40637732450): all three jobs succeeded, including full backend `go test ./...`, Linux package race/vet/coverage checks, manifest inventory, and runner preflight. VFS/RunRoot candidate acceptance remains explicitly NOT RUN.

A follow-up negative case at `e93eef1` keeps a real SQLite transaction active while cleanup authority is absent. It verifies terminalization, failure of only the matching receipt, rejection of later JOURNEY publication, terminal authorizer denial of rollback, and retirement of the pinned physical connection. The selected test exposed and fixed a `database/sql.Conn.Raw` deadlock when evicting a connection while its `sql.Tx` still held the connection lease: terminalize, attempt rollback only to release the Go lease (the terminal authorizer denies it), then return `ErrBadConn` to evict. Security and test reviewers found no P1/P2. [Linux and Windows protocol checks passed](https://github.com/l17728/en-words/actions/runs/40641185775).

The full Linux package/manifest workflow on `e93eef1` [passed all three jobs](https://github.com/l17728/en-words/actions/runs/40641450152), including full backend `go test ./...`, manifest inventory, and runner preflight. Earlier run `74d7951` timed out on the deadlocking version and is not counted as acceptance evidence.

The isolated real-SQL negative test pins four distinct physical connections, verifies unique registry IDs and raw SQLite pointers, and issues a fresh challenge to each. Connection A executes connection B's token; A's receipt fails with denial evidence while B's remains issued. Connection C executes its own bit-flipped, unrelated token; its receipt fails independently. Connection D first consumes its token, then replays it; the first callback succeeds and the replay fails the receipt. Each rejected candidate completes its explicit PREFLIGHT cleanup rollback. A test-level defer also rolls back active transactions (or terminalizes and discards uncertain connections) before releasing pins on fatal assertion paths. These cases cover three rows from docs/78 §5; the rest of the negative matrix remains open.

The initial CGO run exposed a test-fixture wiring error: the local connection registry and controller registry were different instances. The harness now shares one registry before opening either physical connection. Security and test reviewers confirmed the correction and the actual scope of the assertions. On `20eb644`, [Linux and Windows protocol checks passed](https://github.com/l17728/en-words/actions/runs/40642862674), including the CGO test, Go race/vet, web build/typecheck, Playwright discovery, Windows boundaries, and test-only symbol check. The [Linux package/manifest workflow also passed](https://github.com/l17728/en-words/actions/runs/40643025212), including full backend `go test ./...`, race/vet/coverage, manifest and runner preflight. VFS/RunRoot candidate acceptance remains NOT RUN.

The four-connection version at `d01cd84` also verifies one successful token consumption followed by same-challenge replay rejection. Test teardown now handles all active transactions before closing pins, and cleanup defers are registered before incremental connection setup so partial setup cannot leak pins. The Linux and Windows protocol workflow initially exposed a runner-test race: a SIGKILLed descendant sometimes disappeared from `/proc` before the test read its state. The test now treats `ProcessLookupError` like a missing `/proc` entry; other read/parse errors still fail. Security and test reviewers approved the replay sequence and cleanup ordering. [Full Linux package/manifest checks passed](https://github.com/l17728/en-words/actions/runs/40644386162), including full backend `go test ./...`, race/vet/coverage. After the runner-test fix, [Linux and Windows protocol checks passed](https://github.com/l17728/en-words/actions/runs/40644583029), including Go CGO tests on both systems, Playwright discovery, Windows boundaries, race/vet, and production-symbol checks. VFS/RunRoot candidate acceptance remains NOT RUN.

## Pre-token lesson UPDATE negative case (2026-10-29)

The bounded docs/78 §5 pre-token UPDATE case is implemented by `TestFirstAudioMatrixRealSQLRejectsLessonUpdateBeforeTokenConsumption` in `server/internal/api/first_audio_matrix_real_sql_manifest_cgo_test.go`. It executes the frozen 72-argument SQLite trigger/UDF against a temporary SQLite database and verifies UPDATE-before-token-consumption is rejected, the token callback is not reached, the challenge records denial evidence, rollback succeeds, and the row remains unchanged. Design and review are recorded in docs/82 and `review/first-audio-pretoken-update-review-20261029.md`.

Linux and Windows CI passed in [run 40801551667](https://github.com/l17728/en-words/actions/runs/40801551667). This closes only this component-level negative case; the remaining PREFLIGHT negative matrix, production authorization policy, guarded `api.App` integration, browser journey/reset-race coverage, performance matrix, and real classroom pilot remain open.

## docs/78 §5 negative-matrix evidence ledger (2026-10-29)

The tests below are evidence for their named slice only. The direct-call case has its own isolated child process and checks terminal JOURNEY rejection; these tests do not collectively satisfy the required fresh-candidate matrix or guarded-App listener-factory assertions in docs/78 §5.

| docs/78 case | Current evidence | Acceptance boundary |
|---|---|---|
| Wrong token, connection A using B's token, replay | `TestFirstAudioMatrixRealSQLRejectsCrossConnectionWrongAndReplayedTokens` | Isolated real SQLite connections and authorizer cleanup are exercised; not full guarded `api.App` fresh-candidate acceptance. |
| Token consumed in transaction N then replayed on the same connection in N+1 | `TestFirstAudioMatrixRealSQLRejectsCrossConnectionWrongAndReplayedTokens` | Real tracked SQLite UDF, epoch advancement, rollback invalidation, and separate cleanup permits are asserted. This proves the combined rollback-invalidation/epoch-binding behavior, not epoch comparison in isolation or full guarded App acceptance. |
| Isolated challenge epoch mismatch | `TestFirstAudioMatrixProbeChallengeRejectsDifferentTransactionEpoch` | State-machine/unit evidence only; not claimed independently by the cross-transaction replay case. |
| Wrong run ID, lesson ID, generation, context version | Four subcases in `TestFirstAudioMatrixRealSQLRejectsCrossConnectionWrongAndReplayedTokens` | Each mutates one valid-but-different session binding inside the real token-UDF permit callback; the UDF rejects with zero authorizer denials, independent receipts/connections remain unchanged, and each candidate rolls back. Bounded SQL callback evidence only. |
| UPDATE before token consumption | `TestFirstAudioMatrixRealSQLRejectsLessonUpdateBeforeTokenConsumption` | Real SQLite trigger/UDF component case; no production authorizer or App candidate. |
| Direct top-level row-guard UDF call | `TestFirstAudioMatrixRealSQLRejectsDirectRowGuardUDF` | Isolated child executes a registered 72-argument UDF call under TOKEN_CONSUMPTION. Real authorizer records exactly one full empty-source SQLITE_FUNCTION denial; SQLite returns SQLITE_ERROR with authorization-denial text, callback count stays zero, controller terminalizes, closes/retires the uncertain physical connection, and rejects a valid later JOURNEY. Revised Linux and Windows protocol CI passed in [run 40815213939](https://github.com/l17728/en-words/actions/runs/40815213939). The guarded-App listener-factory assertion and full docs/78 acceptance remain open. |
| Wrong trigger-source row-guard UDF call | `TestFirstAudioMatrixRealSQLRejectsWrongTriggerSource` | Isolated child invokes the UDF from `attacker_source_guard`; the exact nonempty-source SQLITE_FUNCTION tuple is denied before callback, terminal evidence is checked, and later JOURNEY is rejected. Linux/Windows CGO CI passed in [run 40817001582](https://github.com/l17728/en-words/actions/runs/40817001582). Component subcase only; listener-factory/guarded-App acceptance remains open. |
| Wrong lesson row UPDATE | `TestFirstAudioMatrixRealSQLRejectsOtherLessonUpdate` | One-use token and typed projection are bound to lesson A; the approved trigger source is allowed but the real row guard rejects lesson B. Rollback and full typed schema/two-row snapshot equality are verified, then JOURNEY publication is rejected. Architecture/security and test-quality review passed; Linux/Windows CGO and full Linux backend CI passed in [protocol run 40819750481](https://github.com/l17728/en-words/actions/runs/40819750481) and [package run 40819760855](https://github.com/l17728/en-words/actions/runs/40819760855). Bounded component case only. |
| Missing cleanup authority with an active SQL transaction | `TestFirstAudioMatrixRealSQLMissingCleanupAuthorityTerminalizesAndDiscardsActiveTransaction` | Real SQL terminalization and physical connection retirement are exercised; not a substitute for per-case full candidate assertions. |
| Remaining docs/78 §5 negatives | No matching acceptance evidence recorded yet | Open: direct-call guarded-App listener-factory assertion; callback replay; corrupted expected OLD/NEW projection; zero-row behavior; missing/replaced UDF or trigger and extra trigger; unregistered/replaced physical connection; extra/omitted callback; projection read error or absent/ambiguous target; cancellation/timeout callback-drain races; and phase-specific rollback denial. |

Do not infer unlisted scenarios from a neighboring test. The still-open cases are: direct-call guarded-App listener-factory assertion; duplicate row callback; corrupted OLD/NEW projection; primary-key/fixed-statement at-most-one-row evidence and zero-row behavior; missing/replaced UDF or trigger and extra trigger; unregistered/replaced connection; extra/omitted callback; projection read error or absent/ambiguous target; callback cancellation/drain races; and phase-specific rollback denial. Close each only after its own reviewed case, observable failure assertions, rollback/snapshot or terminal evidence, and required platform CI pass.

The cross-transaction replay case is implemented by extending `TestFirstAudioMatrixRealSQLRejectsCrossConnectionWrongAndReplayedTokens` to a fifth pinned candidate. A successful token callback in N is followed by confirmed rollback and challenge invalidation; N+1 reuses the same raw connection and token, and the second real UDF callback rejects. Linux/Windows protocol and Linux package CI passed on `ad9bb39`: [protocol run 40805069098](https://github.com/l17728/en-words/actions/runs/40805069098), [package run 40805069147](https://github.com/l17728/en-words/actions/runs/40805069147). This remains a bounded harness case, not a full fresh-candidate/guarded-App acceptance.

The four SQL binding variants extend that same test to nine simultaneously pinned candidates. Each valid-but-different run/lesson/generation/version value is submitted through the real token UDF under a narrowly scoped permit; zero authorizer denials confirms rejection occurred in the UDF. [Linux and Windows protocol CI passed](https://github.com/l17728/en-words/actions/runs/40807107366). This does not close the remaining docs/78 matrix or the guarded App/browser/performance/classroom gates.

## Direct top-level row-guard UDF component subcase (2026-10-29)

The direct-call negative is implemented and reviewed in `docs/85-first-audio-direct-row-guard-call-negative-sdd.md` and `review/first-audio-direct-row-guard-call-review-20261029.md`. The authorizer returns `SQLITE_DENY`; SQLite's statement preparation API surfaces `SQLITE_ERROR` (1) with authorization-denial text (SQLite H12505), so the test distinguishes callback decision from API error. It checks the exact denial tuple and confirms the registered UDF callback never runs. A child process isolates the terminalizing attempt; cleanup then confirms receipt failure, successful physical close, and registry retirement.

The revised direct-call test, including explicit terminal JOURNEY-publication rejection, passed Linux and Windows protocol CI on `4466dc8`: [run 40815213939](https://github.com/l17728/en-words/actions/runs/40815213939). CI also passed Linux race, vet, formatting, production-symbol checks, web build/typecheck, Playwright discovery, and Windows boundary checks. The dispatched [Linux package/backend workflow](https://github.com/l17728/en-words/actions/runs/40815610001) passed all three jobs on `4466dc8`, including full backend `go test ./...`, package race/vet checks, manifest inventory, and runner preflight. This is package-only evidence; it does not claim VFS/RunRoot candidate acceptance. The guarded-App listener-factory assertion and full docs/78 no-listener/App case remain open; so do actual Playwright journey execution, performance matrix, and classroom pilot.

## Wrong trigger-source negative case (2026-10-29)

`TestFirstAudioMatrixRealSQLRejectsWrongTriggerSource` creates an unapproved `BEFORE UPDATE` trigger whose `WHEN` clause invokes the registered row-guard UDF. On a seeded lesson row, the exact no-op UPDATE is denied by the real SQLite authorizer before UDF execution. The test checks one exact `PREFLIGHT/PROBE_SQL/SQLITE_FUNCTION` denial with source `attacker_source_guard`, zero UDF callbacks, failed/incomplete receipt, confirmed physical connection close and registry retirement, and rejection of a later same-run JOURNEY candidate. The original challenge token is zeroed.

The SDD and code passed architecture/security and test-quality review. Commit `225919b` passed Linux and Windows CGO protocol checks, including the selected real-SQL test suite, Linux race/vet/format, and Windows CGO boundaries: https://github.com/l17728/en-words/actions/runs/40817001582 . This closes only the wrong-trigger-source component subcase. The docs/78 matrix, listener-factory assertion, guarded `api.App`, real Playwright journey, performance matrix, and isolated human classroom pilot remain open; this is not full first-audio acceptance.

## Other-lesson UPDATE negative case (2026-10-29)

`TestFirstAudioMatrixRealSQLRejectsOtherLessonUpdate` binds its one-use token and expected typed projection to lesson A, then attempts the approved trigger-source no-op UPDATE on lesson B. The real authorizer records the exact allowed trigger tuple; the test-only row guard runs once, detects B's projection mismatch, and makes the approved trigger abort. The test confirms consumed-but-incomplete then failed receipt state, rollback, full typed schema/two-row snapshot equality, and terminal JOURNEY publication rejection. Token material is wiped immediately after consumption and by an early-failure defer. The fixed test row contract has identical OLD/NEW values as required by the production guard context invariant.

The SDD was approved before implementation; architecture/security and test-quality code reviews passed after fixes. Code commit `a19ffa5` passed [Linux/Windows protocol CI](https://github.com/l17728/en-words/actions/runs/40819750481) and [full Linux package/manifest CI](https://github.com/l17728/en-words/actions/runs/40819760855), including focused CGO checks, Linux race/vet/format, Windows boundaries, and full backend `go test ./...`. This closes one component negative only. The remaining docs/78 matrix, listener-factory assertion, guarded `api.App` journey, real Playwright journey, reset races, performance matrix, and isolated human classroom pilot remain open.
