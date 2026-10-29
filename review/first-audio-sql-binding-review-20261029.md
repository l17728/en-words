# First-Audio SQL Binding Review

Date: 2026-10-29
Scope: docs/84 and four negative callback-binding cases in the isolated CGO SQLite harness. No production authorization policy or route changed.

## Design and code review

- Security: PASS. Each of nine candidates uses a distinct pinned raw SQLite connection and registry ID. The valid binding is checked before the one-use token permit; one valid-but-different field is changed only inside the permitted callback, then restored before cleanup. The real SQL UDF rejects; the SQLite authorizer records no denial.
- Testability: PASS. The run, lesson, generation, and context-version cases each assert one callback, false result, FAILED/denied receipt, no completed receipt, unchanged state/denials and live identity for the other eight candidates, and a separately authorized cleanup rollback. Each transaction registers `t.Cleanup` immediately after BeginTx; function-level teardown remains a backstop.
- Acceptance boundary: These four cases add real-SQL UDF evidence for binding fields only. They do not establish the complete docs/78 matrix, guarded App/JOURNEY, browser, performance, or classroom acceptance.

## Validation

- Local Go execution: unavailable; `go` and `gofmt` are not installed or on PATH in this worktree environment.
- Linux/Windows protocol CI: PASS — [run 40807107366](https://github.com/l17728/en-words/actions/runs/40807107366), including the focused CGO matrix, Linux race/vet, Windows harness boundaries, web build, Playwright discovery, and production-symbol check.
