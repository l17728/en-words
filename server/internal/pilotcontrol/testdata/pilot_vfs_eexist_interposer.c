#define _GNU_SOURCE
#include <dlfcn.h>
#include <errno.h>
#include <fcntl.h>
#include <poll.h>
#include <pthread.h>
#include <stdarg.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/syscall.h>
#include <time.h>
#include <unistd.h>
#include <limits.h>

/* Test-only interposer. It fails closed when path identity, symbols, or the
 * bounded parent protocol cannot be established. */
static int control_fd = -1, ready = 0;
static char target_path[PATH_MAX];
static const char *target_token, *scenario_id;
static _Atomic int injected;
static unsigned long seq;
static _Atomic unsigned long open_calls, open64_calls, openat_calls, openat64_calls, exact_calls;
static pthread_mutex_t protocol_mu = PTHREAD_MUTEX_INITIALIZER;
static int (*real_open)(const char *, int, ...);
static int (*real_open64)(const char *, int, ...);
static int (*real_openat)(int, const char *, int, ...);
static int (*real_openat64)(int, const char *, int, ...);

static int deadline_poll(int fd, short events) {
  struct timespec start, now;
  if (clock_gettime(CLOCK_MONOTONIC, &start) != 0) return -1;
  for (;;) {
    if (clock_gettime(CLOCK_MONOTONIC, &now) != 0) return -1;
    long long elapsed = (long long)(now.tv_sec-start.tv_sec)*1000LL + (now.tv_nsec-start.tv_nsec)/1000000LL;
    int left = 10000 - (int)elapsed;
    if (left <= 0) { errno = ETIMEDOUT; return -1; }
    struct pollfd p = { .fd = fd, .events = events };
    int rc = poll(&p, 1, left);
    if (rc > 0) { if (p.revents & events) return 0; errno = EPROTO; return -1; }
    if (rc == 0) { errno = ETIMEDOUT; return -1; }
    if (errno != EINTR) return -1;
  }
}

static int send_frame_locked(const char *type, unsigned long n, int flags, int result, int err, int qualifying, int symbols_ok) {
  char buf[1024];
  int len = snprintf(buf, sizeof(buf),
      "{\"version\":1,\"scenario_id\":\"%s\",\"seq\":%lu,\"type\":\"%s\",\"target_token\":\"%s\",\"flags\":%d,\"result\":%d,\"errno\":%d,\"qualifying_exclusive_create\":%s,\"symbols_ok\":%s,\"open_calls\":%lu,\"open64_calls\":%lu,\"openat_calls\":%lu,\"openat64_calls\":%lu,\"exact_path_attempts\":%lu}",
      scenario_id ? scenario_id : "", n, type, target_token ? target_token : "", flags, result, err, qualifying ? "true":"false", symbols_ok ? "true":"false", atomic_load(&open_calls), atomic_load(&open64_calls), atomic_load(&openat_calls), atomic_load(&openat64_calls), atomic_load(&exact_calls));
  if (len <= 0 || (size_t)len >= sizeof(buf)) { errno = EMSGSIZE; return -1; }
  ssize_t sent = send(control_fd, buf, (size_t)len, MSG_NOSIGNAL);
  return sent == len ? 0 : -1;
}

static int exchange_locked(const char *type, int flags, int qualifying, int *inject) {
  char buf[512], wanted[256];
  unsigned long n = ++seq;
  if (send_frame_locked(type, n, flags, 0, 0, qualifying, 1) != 0 || deadline_poll(control_fd, POLLIN) != 0) return -1;
  ssize_t got = recv(control_fd, buf, sizeof(buf), 0);
  int want = snprintf(wanted, sizeof(wanted), "{\"version\":1,\"scenario_id\":\"%s\",\"seq\":%lu,\"type\":\"%s\"}", scenario_id, n, qualifying && !atomic_load(&injected) ? "INJECT_EEXIST":"CONTINUE");
  if (got != want || want <= 0 || (size_t)want >= sizeof(wanted) || memcmp(buf, wanted, (size_t)want) != 0) { errno = EPROTO; return -1; }
  *inject = qualifying && !atomic_exchange(&injected, 1);
  return 0;
}

/* Resolve using the canonical parent directory plus leaf. A missing parent,
 * deleted dirfd, truncation, or unresolvable path disables matching. */
static int canonical_at(int dirfd, const char *path, char *out, size_t cap) {
  if (!path || !*path) return -1;
  char joined[PATH_MAX], parent[PATH_MAX], resolved[PATH_MAX];
  if (path[0] == '/') {
    if (strlen(path) >= sizeof(joined)) return -1;
    strcpy(joined, path);
  } else {
    char base[PATH_MAX];
    if (dirfd == AT_FDCWD) {
      if (!getcwd(base, sizeof(base))) return -1;
    } else {
      char proc[64]; snprintf(proc, sizeof(proc), "/proc/self/fd/%d", dirfd);
      ssize_t n = readlink(proc, base, sizeof(base)-1);
      if (n <= 0 || (size_t)n >= sizeof(base)-1) return -1;
      base[n] = 0;
    }
    if (snprintf(joined, sizeof(joined), "%s/%s", base, path) >= (int)sizeof(joined)) return -1;
  }
  char *slash = strrchr(joined, '/');
  if (!slash || !slash[1]) return -1;
  char leaf[NAME_MAX+1];
  if (strlen(slash+1) > NAME_MAX) return -1;
  strcpy(leaf, slash+1);
  size_t plen = (size_t)(slash-joined);
  if (plen == 0) plen = 1;
  if (plen >= sizeof(parent)) return -1;
  memcpy(parent, joined, plen); parent[plen] = 0;
  if (!realpath(parent, resolved)) return -1;
  if (snprintf(out, cap, "%s/%s", resolved, leaf) >= (int)cap) return -1;
  return 0;
}

static int exact_path(int dirfd, const char *path) {
  char canonical[PATH_MAX];
  return canonical_at(dirfd, path, canonical, sizeof(canonical)) == 0 && strcmp(canonical, target_path) == 0;
}
static int qualifies(int flags) { return (flags & O_CREAT) && (flags & O_EXCL) && ((flags & O_TMPFILE) != O_TMPFILE); }

static int report_attempt(int flags, int *inject) {
  int saved = errno, q = qualifies(flags), rc;
  pthread_mutex_lock(&protocol_mu);
  atomic_fetch_add(&exact_calls, 1);
  rc = ready ? exchange_locked("EXCLUSIVE_CREATE_OBSERVED", flags, q, inject) : -1;
  pthread_mutex_unlock(&protocol_mu);
  errno = rc == 0 ? saved : EPROTO;
  return rc;
}
static void report_result(int flags, int result, int error, int injected_result) {
  int saved = errno;
  pthread_mutex_lock(&protocol_mu);
  (void)send_frame_locked(injected_result ? "INJECTED_EEXIST_RESULT":"SYSCALL_RESULT", ++seq, flags, result, error, qualifies(flags), 1);
  pthread_mutex_unlock(&protocol_mu);
  errno = saved;
}

__attribute__((constructor)) static void init_interposer(void) {
  const char *fd = getenv("PILOT_VFS_EEXIST_CONTROL_FD"), *raw = getenv("PILOT_VFS_EEXIST_TARGET_PATH");
  target_token = getenv("PILOT_VFS_EEXIST_TARGET_TOKEN"); scenario_id = getenv("PILOT_VFS_EEXIST_SCENARIO_ID");
  if (fd) control_fd = atoi(fd);
  dlerror(); real_open = dlsym(RTLD_NEXT, "open");
  dlerror(); real_open64 = dlsym(RTLD_NEXT, "open64");
  dlerror(); real_openat = dlsym(RTLD_NEXT, "openat");
  dlerror(); real_openat64 = dlsym(RTLD_NEXT, "openat64");
  ready = raw && target_token && scenario_id && control_fd >= 0 && real_open && real_open64 && real_openat && real_openat64 && canonical_at(AT_FDCWD, raw, target_path, sizeof(target_path)) == 0;
  pthread_mutex_lock(&protocol_mu);
  (void)send_frame_locked(ready ? "READY":"UNAVAILABLE", ++seq, 0, 0, ready ? 0:ENOSYS, 0, ready);
  pthread_mutex_unlock(&protocol_mu);
}

#define WRAP_OPEN(name, realfn, count) \
int name(const char *path, int flags, ...) { \
  mode_t mode=0; int has_mode=(flags&O_CREAT)!=0 || ((flags&O_TMPFILE)==O_TMPFILE); \
  if (has_mode) { va_list ap; va_start(ap,flags); mode=(mode_t)va_arg(ap,int); va_end(ap); } \
  atomic_fetch_add(&count,1); if (!realfn) { errno=ENOSYS; return -1; } int saved=errno; int inject=0, matched=exact_path(AT_FDCWD,path); errno=saved; \
  if (matched && report_attempt(flags,&inject)!=0) { errno=EPROTO; return -1; } \
  if (inject) { report_result(flags,-1,EEXIST,1); errno=EEXIST; return -1; } \
  int r = has_mode ? realfn(path,flags,mode) : realfn(path,flags); int e=errno; \
  if (matched) report_result(flags,r,r<0?e:0,0); errno=e; return r; \
}
WRAP_OPEN(open, real_open, open_calls)
WRAP_OPEN(open64, real_open64, open64_calls)

#define WRAP_OPENAT(name, realfn, count) \
int name(int dirfd,const char *path,int flags,...) { \
  mode_t mode=0; int has_mode=(flags&O_CREAT)!=0 || ((flags&O_TMPFILE)==O_TMPFILE); \
  if (has_mode) { va_list ap; va_start(ap,flags); mode=(mode_t)va_arg(ap,int); va_end(ap); } \
  atomic_fetch_add(&count,1); if (!realfn) { errno=ENOSYS; return -1; } int saved=errno; int inject=0, matched=exact_path(dirfd,path); errno=saved; \
  if (matched && report_attempt(flags,&inject)!=0) { errno=EPROTO; return -1; } \
  if (inject) { report_result(flags,-1,EEXIST,1); errno=EEXIST; return -1; } \
  int r = has_mode ? realfn(dirfd,path,flags,mode) : realfn(dirfd,path,flags); int e=errno; \
  if (matched) report_result(flags,r,r<0?e:0,0); errno=e; return r; \
}
WRAP_OPENAT(openat, real_openat, openat_calls)
WRAP_OPENAT(openat64, real_openat64, openat64_calls)
