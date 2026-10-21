# Pilot Privileged Linux Runner Preflight — 2026-10-22

## Observed on remote host

These are operator-observed facts from one read-only SSH preflight, not a provisioned-runner acceptance result. Exact command, captured stdout/stderr, and exit code are retained in `review/pilot-privileged-linux-preflight-evidence-20261022/` (`command.txt`, `output.log`, `exit.txt`; exit `0`).

Read-only inspection over the authorized SSH connection to `124.156.193.122` reported Linux amd64 (`5.15.0-171-generic`), `/dev/vda2` ext4 root, systemd active as PID 1, unified cgroup v2 mounted read-write, root cgroup controllers `cpuset cpu io memory hugetlb pids rdma misc`, and successful `unshare -m true`. The host has existing `ubuntu` UID 1000 and `picer` UID 998 accounts. The current SSH session is root.

## Not established / unavailable

The captured lookup queried only the existing `ubuntu` and `picer` accounts; it did not establish whether other service accounts exist. This preflight has not confirmed dedicated, pre-provisioned launcher, Logger, and Reaper UIDs/GIDs. The inspection did not create accounts, a delegated cgroup subtree, a systemd unit, or a private mount fixture. `clone3(CLONE_PIDFD|CLONE_INTO_CGROUP)`, pidfd `waitid`, exact process placement before first instruction, protected log mount, and amd64/arm64 matrix have not been demonstrated. The host is not declared a disposable privileged-test VM by this evidence. Therefore docs/47–48 privileged acceptance remains **BLOCKED**, despite the usable kernel primitives and root session.

## Gate decision

Do not count root-only socket/process tests or the package-level Linux run as privileged launcher/logger acceptance. Continue only after a disposable, versioned runner is provisioned with distinct service identities and the docs/47–48 machine-readable preflight passes. No shared-host accounts or services were changed for this inspection.



