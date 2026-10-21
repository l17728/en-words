//go:build cgo && pilot_e2e && linux

#include <pthread.h>
#include <poll.h>
#include <stdint.h>
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <signal.h>
#include <time.h>
#include <unistd.h>

#include "../../third_party/go-sqlite3/sqlite3-binding.h"

typedef struct PilotTraceFile PilotTraceFile;
struct PilotTraceFile {
  sqlite3_file base;
  sqlite3_file *real;
  sqlite3_io_methods methods;
  int handle_id;
  int flags;
  char name[512];
  char role[32];
};

static sqlite3_vfs g_parent;
static sqlite3_vfs g_trace_vfs;
static char g_trace_name[128];
static char g_run_id[96] = "unset";
static char g_scenario_id[96] = "unset";
static char g_case_id[96] = "unset";
static char g_source_id[160] = "unset";
static char g_parent_name[128] = "unset";
static int g_registered = 0;
static int g_next_handle = 0;
static unsigned long long g_seq = 0;
static char g_events[4 * 1024 * 1024];
static size_t g_events_len = 0;
static int g_events_overflow = 0;
static pthread_mutex_t g_mu = PTHREAD_MUTEX_INITIALIZER;
static __thread char g_actor[80] = "unassigned";
static int g_pager_control_fd = -1;
static int g_pager_trace_fd = -1;
static size_t g_events_flushed_len = 0;
static int g_pager_journal_synced = 0;
static int g_pager_main_write_blocked = 0;
static char g_pager_scenario[96] = "";

static int write_all(int fd, const char *data, size_t len) {
  size_t off = 0;
  while (off < len) {
    ssize_t n = write(fd, data + off, len - off);
    if (n < 0) return -1;
    if (n == 0) return -1;
    off += (size_t)n;
  }
  return 0;
}

static long long pager_now_ms(void) {
  struct timespec ts;
  if (clock_gettime(CLOCK_MONOTONIC, &ts) != 0) return -1;
  return (long long)ts.tv_sec * 1000LL + ts.tv_nsec / 1000000LL;
}

static int pager_wait_fd(int fd, short events, long long deadline_ms) {
  for (;;) {
    long long now = pager_now_ms();
    if (now < 0) return -1;
    long long left = deadline_ms - now;
    if (left <= 0) { errno = ETIMEDOUT; return -1; }
    struct pollfd pfd;
    int rc;
    pfd.fd = fd; pfd.events = events; pfd.revents = 0;
    rc = poll(&pfd, 1, left > 2147483647LL ? 2147483647 : (int)left);
    if (rc > 0) {
      if (pfd.revents & events) return 0;
      errno = (pfd.revents & (POLLHUP|POLLERR|POLLNVAL)) ? EPIPE : EPROTO;
      return -1;
    }
    if (rc == 0) { errno = ETIMEDOUT; return -1; }
    if (errno != EINTR) return -1;
  }
}

static int pager_deadline_expired(long long deadline_ms) {
  long long now = pager_now_ms();
  if (now < 0) { errno = EIO; return 1; }
  if (now >= deadline_ms) { errno = ETIMEDOUT; return 1; }
  return 0;
}

static int trace_flush_durable(void) {
  char *copy;
  size_t start, end, len;
  int rc;
  if (g_pager_trace_fd < 0) return -1;
  pthread_mutex_lock(&g_mu);
  start = g_events_flushed_len;
  end = g_events_len;
  len = end - start;
  copy = (char *)malloc(len ? len : 1);
  if (copy && len) memcpy(copy, g_events + start, len);
  pthread_mutex_unlock(&g_mu);
  if (!copy) return -1;
  rc = write_all(g_pager_trace_fd, copy, len);
  free(copy);
  if (rc != 0 || fdatasync(g_pager_trace_fd) != 0) return -1;
  pthread_mutex_lock(&g_mu);
  if (g_events_flushed_len < end) g_events_flushed_len = end;
  pthread_mutex_unlock(&g_mu);
  return 0;
}

static int pager_barrier_send_wait(const char *type, unsigned long long seq,
                                   int handle, long long offset, int size,
                                   int delegate_rc, int callback_returned,
                                   const char *reply_type) {
  char frame[1024], expected[1024], reply[1024];
  int n, expected_n;
  long long deadline_ms = pager_now_ms();
  ssize_t got;
  if (deadline_ms < 0) return -1;
  deadline_ms += 10000;
  if (g_pager_control_fd < 0 || g_pager_scenario[0] == 0) return -1;
  n = snprintf(frame, sizeof(frame),
    "{\"version\":1,\"scenario_id\":\"%s\",\"seq\":%llu,\"type\":\"%s\",\"handle_token\":%d,\"offset\":%lld,\"size\":%d,\"delegate_rc\":%d,\"callback_returned\":%s}",
    g_pager_scenario, seq, type, handle, offset, size, delegate_rc,
    callback_returned ? "true" : "false");
  if (n <= 0 || (size_t)n >= sizeof(frame) || pager_wait_fd(g_pager_control_fd, POLLOUT, deadline_ms) != 0) return -1;
  for (;;) {
    got = send(g_pager_control_fd, frame, (size_t)n, MSG_NOSIGNAL);
    if (got == n) break;
    if (got >= 0 || errno != EINTR || pager_deadline_expired(deadline_ms)) return -1;
  }
  if (pager_wait_fd(g_pager_control_fd, POLLIN, deadline_ms) != 0) return -1;
  for (;;) {
    got = recv(g_pager_control_fd, reply, sizeof(reply) - 1, 0);
    if (got >= 0) break;
    if (errno != EINTR || pager_deadline_expired(deadline_ms)) return -1;
  }
  if (got <= 0) return -1;
  reply[got] = 0;
  expected_n = snprintf(expected, sizeof(expected),
    "{\"version\":1,\"scenario_id\":\"%s\",\"seq\":%llu,\"type\":\"%s\"}",
    g_pager_scenario, seq, reply_type);
  if (expected_n <= 0 || (size_t)expected_n >= sizeof(expected) || got != expected_n || memcmp(reply, expected, (size_t)got) != 0) return -1;
  if (strcmp(reply_type,"KILL_ACK")==0) {
    for (;;) pause();
  }
  return 0;
}

static const char *trace_base(const char *name) {
  const char *p;
  if (!name) return "";
  p = strrchr(name, '/');
  return p ? p + 1 : name;
}

static const char *trace_role(int flags) {
  if (flags & SQLITE_OPEN_MAIN_DB) return "MAIN_DB";
  if (flags & SQLITE_OPEN_WAL) return "WAL";
  if (flags & SQLITE_OPEN_MAIN_JOURNAL) return "MAIN_JOURNAL";
  if (flags & SQLITE_OPEN_TEMP_JOURNAL) return "TEMP_JOURNAL";
  if (flags & SQLITE_OPEN_SUBJOURNAL) return "SUBJOURNAL";
  if (flags & SQLITE_OPEN_SUPER_JOURNAL) return "SUPER_JOURNAL";
  if (flags & SQLITE_OPEN_TEMP_DB) return "TEMP_DB";
  if (flags & SQLITE_OPEN_TRANSIENT_DB) return "TRANSIENT_DB";
  return "OTHER";
}

static int trace_escape(char *out, size_t cap, const char *in) {
  size_t n = 0;
  const unsigned char *p = (const unsigned char *)(in ? in : "");
  while (*p && n + 2 < cap) {
    if (*p == '"' || *p == '\\') { out[n++] = '\\'; out[n++] = (char)*p++; }
    else if (*p < 0x20) { out[n++] = ' '; p++; }
    else out[n++] = (char)*p++;
  }
  out[n] = 0;
  return *p != 0;
}

static void trace_event_full(const char *callback, int handle, const char *name,
                             const char *role, int a, int b, int c, int rc, int out,
                             const char *output_name) {
  char ename[512], erole[48], eactor[96], erun[112], escenario[112], ecase[112], esource[176];
  char eoutput[512], handle_json[32], output_json[1024];
  int truncated = 0;
  truncated |= trace_escape(ename, sizeof(ename), trace_base(name));
  truncated |= trace_escape(erole, sizeof(erole), role);
  truncated |= trace_escape(eactor, sizeof(eactor), g_actor);
  if (handle > 0) snprintf(handle_json, sizeof(handle_json), "%d", handle);
  else snprintf(handle_json, sizeof(handle_json), "null");
  if (output_name) {
    truncated |= trace_escape(eoutput, sizeof(eoutput), output_name);
    snprintf(output_json, sizeof(output_json), "\"%s\"", eoutput);
  } else {
    snprintf(output_json, sizeof(output_json), "null");
  }
  pthread_mutex_lock(&g_mu);
  truncated |= trace_escape(erun, sizeof(erun), g_run_id);
  truncated |= trace_escape(escenario, sizeof(escenario), g_scenario_id);
  truncated |= trace_escape(ecase, sizeof(ecase), g_case_id);
  truncated |= trace_escape(esource, sizeof(esource), g_source_id);
  if (truncated) g_events_overflow = 1;
  if (g_events_len + 1024 < sizeof(g_events)) {
    int n = snprintf(g_events + g_events_len, sizeof(g_events) - g_events_len,
      "{\"run_id\":\"%s\",\"scenario_id\":\"%s\",\"case_id\":\"%s\",\"source_id\":\"%s\",\"seq\":%llu,\"connection_id\":\"%s\",\"actor\":\"%s\",\"callback\":\"%s\",\"file_handle_id\":%s,\"name\":\"%s\",\"output_name\":%s,\"role\":\"%s\",\"arg0\":%d,\"arg1\":%d,\"arg2\":%d,\"rc\":%d,\"out\":%d}\n",
      erun, escenario, ecase, esource, ++g_seq, eactor, eactor, callback, handle_json, ename, output_json, erole, a, b, c, rc, out);
    if (n > 0 && (size_t)n < sizeof(g_events) - g_events_len) g_events_len += (size_t)n;
    else g_events_overflow = 1;
  } else g_events_overflow = 1;
  pthread_mutex_unlock(&g_mu);
}
static void trace_event(const char *callback, int handle, const char *name,
                        const char *role, int a, int b, int c, int rc, int out) {
  trace_event_full(callback, handle, name, role, a, b, c, rc, out, NULL);
}

static int tfClose(sqlite3_file *f) {
  PilotTraceFile *t = (PilotTraceFile *)f;
  int rc = t->real->pMethods->xClose(t->real);
  trace_event("xClose", t->handle_id, t->name, t->role, 0, 0, 0, rc, 0);
  sqlite3_free(t->real); t->real = 0; f->pMethods = 0;
  return rc;
}
static int tfRead(sqlite3_file *f, void *buf, int n, sqlite3_int64 off) {
  PilotTraceFile *t=(PilotTraceFile*)f; int rc=t->real->pMethods->xRead(t->real,buf,n,off);
  trace_event("xRead",t->handle_id,t->name,t->role,n,(int)off,(int)(off>>32),rc,0); return rc;
}
static int tfWrite(sqlite3_file *f, const void *buf, int n, sqlite3_int64 off) {
  PilotTraceFile *t=(PilotTraceFile*)f; int rc=t->real->pMethods->xWrite(t->real,buf,n,off);
  trace_event("xWrite",t->handle_id,t->name,t->role,n,(int)off,(int)(off>>32),rc,0);
  if (g_pager_journal_synced && !g_pager_main_write_blocked && strcmp(t->role,"MAIN_DB")==0 && n>0 && off>=0 && rc==SQLITE_OK) {
    g_pager_main_write_blocked = 1;
    if (trace_flush_durable() != 0 || pager_barrier_send_wait("MAIN_WRITE_DELEGATED_OK",2,t->handle_id,(long long)off,n,rc,0,"KILL_ACK") != 0) return SQLITE_IOERR_WRITE;
  }
  return rc;
}
static int tfTruncate(sqlite3_file *f, sqlite3_int64 size) {
  PilotTraceFile *t=(PilotTraceFile*)f; int rc=t->real->pMethods->xTruncate(t->real,size);
  trace_event("xTruncate",t->handle_id,t->name,t->role,(int)size,(int)(size>>32),0,rc,0); return rc;
}
static int tfSync(sqlite3_file *f, int flags) {
  PilotTraceFile *t=(PilotTraceFile*)f; int rc=t->real->pMethods->xSync(t->real,flags);
  trace_event("xSync",t->handle_id,t->name,t->role,flags,0,0,rc,0);
  if (g_pager_control_fd>=0 && !g_pager_journal_synced && strcmp(t->role,"MAIN_JOURNAL")==0 && rc==SQLITE_OK) {
    if (trace_flush_durable() != 0 || pager_barrier_send_wait("JOURNAL_SYNC_OK",1,t->handle_id,0,0,rc,0,"CONTINUE") != 0) return SQLITE_IOERR_FSYNC;
    g_pager_journal_synced=1;
  }
  return rc;
}
static int tfFileSize(sqlite3_file *f, sqlite3_int64 *size) {
  PilotTraceFile *t=(PilotTraceFile*)f; int rc=t->real->pMethods->xFileSize(t->real,size);
  trace_event("xFileSize",t->handle_id,t->name,t->role,rc==SQLITE_OK?(int)*size:0,rc==SQLITE_OK?(int)(*size>>32):0,0,rc,0); return rc;
}
static int tfLock(sqlite3_file *f, int level) {
  PilotTraceFile *t=(PilotTraceFile*)f; int rc=t->real->pMethods->xLock(t->real,level);
  trace_event("xLock",t->handle_id,t->name,t->role,level,0,0,rc,0); return rc;
}
static int tfUnlock(sqlite3_file *f, int level) {
  PilotTraceFile *t=(PilotTraceFile*)f; int rc=t->real->pMethods->xUnlock(t->real,level);
  trace_event("xUnlock",t->handle_id,t->name,t->role,level,0,0,rc,0); return rc;
}
static int tfCheckReserved(sqlite3_file *f, int *reserved) {
  PilotTraceFile *t=(PilotTraceFile*)f; int rc=t->real->pMethods->xCheckReservedLock(t->real,reserved);
  trace_event("xCheckReservedLock",t->handle_id,t->name,t->role,0,0,0,rc,rc==SQLITE_OK && reserved ? *reserved : -1); return rc;
}
static int tfFileControl(sqlite3_file *f, int op, void *arg) {
  PilotTraceFile *t=(PilotTraceFile*)f; int rc=t->real->pMethods->xFileControl(t->real,op,arg);
  trace_event("xFileControl",t->handle_id,t->name,t->role,op,0,0,rc,0); return rc;
}
static int tfSectorSize(sqlite3_file *f) {
  PilotTraceFile *t=(PilotTraceFile*)f; int out=t->real->pMethods->xSectorSize(t->real);
  trace_event("xSectorSize",t->handle_id,t->name,t->role,0,0,0,SQLITE_OK,out); return out;
}
static int tfDeviceCharacteristics(sqlite3_file *f) {
  PilotTraceFile *t=(PilotTraceFile*)f; int out=t->real->pMethods->xDeviceCharacteristics(t->real);
  trace_event("xDeviceCharacteristics",t->handle_id,t->name,t->role,0,0,0,SQLITE_OK,out); return out;
}
static int tfShmMap(sqlite3_file *f, int region, int size, int extend, void volatile **out) {
  PilotTraceFile *t=(PilotTraceFile*)f; int rc=t->real->pMethods->xShmMap(t->real,region,size,extend,out);
  trace_event("xShmMap",t->handle_id,t->name,t->role,region,size,extend,rc,rc==SQLITE_OK && out && *out!=0); return rc;
}
static int tfShmLock(sqlite3_file *f, int offset, int n, int flags) {
  PilotTraceFile *t=(PilotTraceFile*)f; int rc=t->real->pMethods->xShmLock(t->real,offset,n,flags);
  trace_event("xShmLock",t->handle_id,t->name,t->role,offset,n,flags,rc,0); return rc;
}
static void tfShmBarrier(sqlite3_file *f) {
  PilotTraceFile *t=(PilotTraceFile*)f; t->real->pMethods->xShmBarrier(t->real);
  trace_event("xShmBarrier",t->handle_id,t->name,t->role,0,0,0,SQLITE_OK,0);
}
static int tfShmUnmap(sqlite3_file *f, int deleteFlag) {
  PilotTraceFile *t=(PilotTraceFile*)f; int rc=t->real->pMethods->xShmUnmap(t->real,deleteFlag);
  trace_event("xShmUnmap",t->handle_id,t->name,t->role,deleteFlag,0,0,rc,0); return rc;
}
static int tfFetch(sqlite3_file *f, sqlite3_int64 off, int n, void **out) {
  PilotTraceFile *t=(PilotTraceFile*)f; int rc=t->real->pMethods->xFetch(t->real,off,n,out);
  trace_event("xFetch",t->handle_id,t->name,t->role,n,(int)off,(int)(off>>32),rc,rc==SQLITE_OK && out && *out!=0); return rc;
}
static int tfUnfetch(sqlite3_file *f, sqlite3_int64 off, void *p) {
  PilotTraceFile *t=(PilotTraceFile*)f; int rc=t->real->pMethods->xUnfetch(t->real,off,p);
  trace_event("xUnfetch",t->handle_id,t->name,t->role,(int)off,(int)(off>>32),0,rc,0); return rc;
}

static int tvOpen(sqlite3_vfs *v, sqlite3_filename name, sqlite3_file *f, int flags, int *outFlags) {
  PilotTraceFile *t=(PilotTraceFile*)f; int rc; const sqlite3_io_methods *m;
  memset(t,0,sizeof(*t)); t->real=(sqlite3_file*)sqlite3_malloc(g_parent.szOsFile);
  if (!t->real) return SQLITE_NOMEM;
  memset(t->real,0,g_parent.szOsFile); t->handle_id=__sync_add_and_fetch(&g_next_handle,1); t->flags=flags;
  snprintf(t->name,sizeof(t->name),"%s",trace_base(name)); snprintf(t->role,sizeof(t->role),"%s",trace_role(flags));
  rc=g_parent.xOpen(&g_parent,name,t->real,flags,outFlags);
  if (rc==SQLITE_OK && (m=t->real->pMethods)!=0) {
    t->methods=*m; t->methods.xClose=tfClose; t->methods.xRead=tfRead; t->methods.xWrite=tfWrite;
    t->methods.xTruncate=tfTruncate; t->methods.xSync=tfSync; t->methods.xFileSize=tfFileSize;
    t->methods.xLock=tfLock; t->methods.xUnlock=tfUnlock; t->methods.xCheckReservedLock=tfCheckReserved;
    t->methods.xFileControl=tfFileControl; t->methods.xSectorSize=tfSectorSize; t->methods.xDeviceCharacteristics=tfDeviceCharacteristics;
    if (m->iVersion>=2 && m->xShmMap) t->methods.xShmMap=tfShmMap;
    if (m->iVersion>=2 && m->xShmLock) t->methods.xShmLock=tfShmLock;
    if (m->iVersion>=2 && m->xShmBarrier) t->methods.xShmBarrier=tfShmBarrier;
    if (m->iVersion>=2 && m->xShmUnmap) t->methods.xShmUnmap=tfShmUnmap;
    if (m->iVersion>=3 && m->xFetch) t->methods.xFetch=tfFetch;
    if (m->iVersion>=3 && m->xUnfetch) t->methods.xUnfetch=tfUnfetch;
    f->pMethods=&t->methods;
  } else { sqlite3_free(t->real); t->real=0; }
  trace_event("xOpen",rc==SQLITE_OK?t->handle_id:0,name,t->role,flags,0,0,rc,rc==SQLITE_OK && outFlags ? *outFlags : 0);
  return rc;
}
static int tvDelete(sqlite3_vfs *v,const char *name,int syncDir) { int rc=g_parent.xDelete(&g_parent,name,syncDir); trace_event("xDelete",0,name,"",syncDir,0,0,rc,0); return rc; }
static int tvAccess(sqlite3_vfs *v,const char *name,int flags,int *out) { int rc=g_parent.xAccess(&g_parent,name,flags,out); trace_event("xAccess",0,name,"",flags,0,0,rc,rc==SQLITE_OK && out ? *out : -1); return rc; }
static int tvFullPath(sqlite3_vfs *v,const char *name,int n,char *out) { int rc=g_parent.xFullPathname(&g_parent,name,n,out); trace_event_full("xFullPathname",0,name,"",n,0,0,rc,0,rc==SQLITE_OK?trace_base(out):NULL); return rc; }
static int tvRandomness(sqlite3_vfs *v,int n,char *out) { int rc=g_parent.xRandomness(&g_parent,n,out); trace_event("xRandomness",0,"","",n,0,0,rc,0); return rc; }
static int tvSleep(sqlite3_vfs *v,int us) { int rc=g_parent.xSleep(&g_parent,us); trace_event("xSleep",0,"","",us,0,0,rc,0); return rc; }
static int tvCurrentTime(sqlite3_vfs *v,double *out) { int rc=g_parent.xCurrentTime(&g_parent,out); trace_event("xCurrentTime",0,"","",0,0,0,rc,0); return rc; }
static int tvGetLastError(sqlite3_vfs *v,int n,char *out) { int rc=g_parent.xGetLastError(&g_parent,n,out); trace_event("xGetLastError",0,"","",n,0,0,rc,0); return rc; }
static int tvCurrentTimeInt64(sqlite3_vfs *v,sqlite3_int64 *out) { int rc=g_parent.xCurrentTimeInt64(&g_parent,out); trace_event("xCurrentTimeInt64",0,"","",0,0,0,rc,0); return rc; }
static void *tvDlOpen(sqlite3_vfs *v,const char *name) { return g_parent.xDlOpen ? g_parent.xDlOpen(&g_parent,name) : NULL; }
static void tvDlError(sqlite3_vfs *v,int n,char *out) { if (g_parent.xDlError) g_parent.xDlError(&g_parent,n,out); }
static void (*tvDlSym(sqlite3_vfs *v,void *handle,const char *symbol))(void) { return g_parent.xDlSym ? g_parent.xDlSym(&g_parent,handle,symbol) : NULL; }
static void tvDlClose(sqlite3_vfs *v,void *handle) { if (g_parent.xDlClose) g_parent.xDlClose(&g_parent,handle); }
static int tvSetSystemCall(sqlite3_vfs *v,const char *name,sqlite3_syscall_ptr call) { return g_parent.xSetSystemCall ? g_parent.xSetSystemCall(&g_parent,name,call) : SQLITE_NOTFOUND; }
static sqlite3_syscall_ptr tvGetSystemCall(sqlite3_vfs *v,const char *name) { return g_parent.xGetSystemCall ? g_parent.xGetSystemCall(&g_parent,name) : NULL; }
static const char *tvNextSystemCall(sqlite3_vfs *v,const char *name) { return g_parent.xNextSystemCall ? g_parent.xNextSystemCall(&g_parent,name) : NULL; }

int pilot_trace_register(const char *name, const char *expected_parent) {
  int rc;
  if (g_registered) return SQLITE_MISUSE;
  sqlite3_initialize();
  sqlite3_vfs *parent=sqlite3_vfs_find(0);
  if (!parent || parent->iVersion<2 || parent->iVersion>3) return SQLITE_ERROR;
  if (!parent->zName || !expected_parent || strcmp(parent->zName,expected_parent)!=0) return SQLITE_MISMATCH;
  snprintf(g_parent_name,sizeof(g_parent_name),"%s",parent->zName);
  g_parent=*parent;
  g_trace_vfs=*parent;
  g_trace_vfs.pNext=NULL;
  g_trace_vfs.iVersion=parent->iVersion; g_trace_vfs.szOsFile=(int)sizeof(PilotTraceFile); g_trace_vfs.mxPathname=parent->mxPathname;
  snprintf(g_trace_name,sizeof(g_trace_name),"%s",name?name:"pilot_vfs_trace");
  g_trace_vfs.zName=g_trace_name; g_trace_vfs.pAppData=parent->pAppData;
  g_trace_vfs.xOpen=tvOpen; g_trace_vfs.xDelete=tvDelete; g_trace_vfs.xAccess=tvAccess; g_trace_vfs.xFullPathname=tvFullPath;
  g_trace_vfs.xDlOpen=parent->xDlOpen?tvDlOpen:NULL; g_trace_vfs.xDlError=parent->xDlError?tvDlError:NULL;
  g_trace_vfs.xDlSym=parent->xDlSym?tvDlSym:NULL; g_trace_vfs.xDlClose=parent->xDlClose?tvDlClose:NULL;
  g_trace_vfs.xRandomness=parent->xRandomness?tvRandomness:NULL;
  g_trace_vfs.xSleep=parent->xSleep?tvSleep:NULL;
  g_trace_vfs.xCurrentTime=parent->xCurrentTime?tvCurrentTime:NULL;
  g_trace_vfs.xGetLastError=parent->xGetLastError?tvGetLastError:NULL;
  g_trace_vfs.xCurrentTimeInt64=parent->xCurrentTimeInt64?tvCurrentTimeInt64:NULL;
  if (parent->iVersion>=3) {
    g_trace_vfs.xSetSystemCall=parent->xSetSystemCall?tvSetSystemCall:NULL;
    g_trace_vfs.xGetSystemCall=parent->xGetSystemCall?tvGetSystemCall:NULL;
    g_trace_vfs.xNextSystemCall=parent->xNextSystemCall?tvNextSystemCall:NULL;
  }
  rc=sqlite3_vfs_register(&g_trace_vfs,0); if (rc==SQLITE_OK) g_registered=1; return rc;
}
const char *pilot_trace_parent_name(void) { return g_parent_name; }
int pilot_trace_capabilities_preserved(void) {
  if (!g_registered || g_trace_vfs.iVersion != g_parent.iVersion) return 0;
  if ((g_parent.xDlOpen && !g_trace_vfs.xDlOpen) || (g_parent.xDlError && !g_trace_vfs.xDlError) ||
      (g_parent.xDlSym && !g_trace_vfs.xDlSym) || (g_parent.xDlClose && !g_trace_vfs.xDlClose) ||
      (g_parent.xRandomness && !g_trace_vfs.xRandomness) || (g_parent.xSleep && !g_trace_vfs.xSleep) ||
      (g_parent.xCurrentTime && !g_trace_vfs.xCurrentTime) || (g_parent.xGetLastError && !g_trace_vfs.xGetLastError)) return 0;
  if (g_parent.iVersion >= 2 && ((g_parent.xCurrentTimeInt64 && !g_trace_vfs.xCurrentTimeInt64))) return 0;
  if (g_parent.iVersion >= 3 && ((g_parent.xSetSystemCall && !g_trace_vfs.xSetSystemCall) ||
      (g_parent.xGetSystemCall && !g_trace_vfs.xGetSystemCall) || (g_parent.xNextSystemCall && !g_trace_vfs.xNextSystemCall))) return 0;
  return 1;
}
int pilot_trace_unregister(void) { int rc; if (!g_registered) return SQLITE_OK; rc=sqlite3_vfs_unregister(&g_trace_vfs); if (rc==SQLITE_OK) g_registered=0; return rc; }
void pilot_trace_reset(void) { pthread_mutex_lock(&g_mu); g_events_len=0; g_events_flushed_len=0; g_events[0]=0; g_events_overflow=0; pthread_mutex_unlock(&g_mu); }
int pilot_trace_overflowed(void) { int overflow; pthread_mutex_lock(&g_mu); overflow=g_events_overflow; pthread_mutex_unlock(&g_mu); return overflow; }
void pilot_trace_test_set_overflowed(int overflow) { pthread_mutex_lock(&g_mu); g_events_overflow=overflow?1:0; pthread_mutex_unlock(&g_mu); }
void pilot_trace_set_actor(const char *actor) {
  const char *value=actor?actor:"unassigned";
  snprintf(g_actor,sizeof(g_actor),"%s",value);
  if (strlen(value)>=sizeof(g_actor)) {
    pthread_mutex_lock(&g_mu); g_events_overflow=1; pthread_mutex_unlock(&g_mu);
  }
}
void pilot_trace_set_context(const char *run_id,const char *scenario_id,const char *case_id,const char *source_id) {
  pthread_mutex_lock(&g_mu);
  if ((run_id && strlen(run_id)>=sizeof(g_run_id)) ||
      (scenario_id && strlen(scenario_id)>=sizeof(g_scenario_id)) ||
      (case_id && strlen(case_id)>=sizeof(g_case_id)) ||
      (source_id && strlen(source_id)>=sizeof(g_source_id))) g_events_overflow=1;
  snprintf(g_run_id,sizeof(g_run_id),"%s",run_id?run_id:"unset");
  snprintf(g_scenario_id,sizeof(g_scenario_id),"%s",scenario_id?scenario_id:"unset");
  snprintf(g_case_id,sizeof(g_case_id),"%s",case_id?case_id:"unset");
  snprintf(g_source_id,sizeof(g_source_id),"%s",source_id?source_id:"unset");
  pthread_mutex_unlock(&g_mu);
}
int pilot_trace_copy(char *out, int cap) {
  size_t n;
  if (!out || cap <= 0) return -1;
  pthread_mutex_lock(&g_mu);
  n = g_events_len;
  if (n >= (size_t)cap) n = (size_t)cap - 1;
  memcpy(out, g_events, n);
  out[n] = 0;
  pthread_mutex_unlock(&g_mu);
  return (int)n;
}

void pilot_trace_set_pager_barrier(int control_fd, int trace_fd, const char *scenario) {
  g_pager_control_fd = control_fd;
  g_pager_trace_fd = trace_fd;
  g_pager_journal_synced = 0;
  g_pager_main_write_blocked = 0;
  g_events_flushed_len = 0;
  snprintf(g_pager_scenario,sizeof(g_pager_scenario),"%s",scenario?scenario:"");
}

int pilot_trace_flush_durable(void) { return trace_flush_durable(); }
