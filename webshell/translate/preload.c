/*
 * libdvps-t.so — docker-vps T 層轉譯層（LD_PRELOAD 路徑重寫）
 *
 * 語義：白名單頂層路徑 /xxx → <DATA_ROOT>/rootfs/xxx 透明重定向；
 *       getcwd 反向重寫保持 shell 顯示虛擬路徑；cd / 進入卷根。
 * 排除：/proc /sys /dev /run /tmp /data /etc/{resolv.conf,hostname,hosts,mtab}
 * 事實源：單一 /data/rootfs；容器根白名單目錄在首啟 seed 後不再被寫入。
 * 不攔截 ld.so（靜態讀取鏡像層 /etc/ld.so.preload 與 /usr/lib 副本）。
 * 靜態二進制（含 webshell Go，CGO_ENABLED=0）不受影響。
 */
#define _GNU_SOURCE
#include <dlfcn.h>
#include <errno.h>
#include <fcntl.h>
#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <sys/types.h>
#include <unistd.h>
#include <limits.h>
#include <dirent.h>
#include <sys/time.h>
#include <utime.h>

/* ---------- 配置（_init 時凍結，之後只讀，線程安全） ---------- */

static char g_prefix[PATH_MAX];   /* 例：/data/rootfs */
static size_t g_prefix_len;
static int g_enabled;             /* 0 = 直通（無卷/未 seed） */

/* 頂層白名單：這些 /xxx 重定向進卷 */
static const char *const g_top[] = {
    "usr", "var", "etc", "root", "home", "opt",
    "bin", "sbin", "lib", "lib64", "srv", NULL,
};

/* /etc 下 Docker bind mount 管理文件：讀寫直達真路徑。
 * 另含 13 個帳號文件（v1.2.1 教訓在 T 層復活）：nss_files 與 ld.so 對
 * /etc/passwd 等的讀取走 libc 內部 open（不經 PLT，無法攔截），所以這些
 * 文件必須在容器層保持真身；rootfs.go initDataRoot 每次啟動做雙向 mtime
 * 同步（新者勝）保證卷側持久。 */
static const char *const g_etc_keep[] = {
    "resolv.conf", "hostname", "hosts", "mtab",
    "passwd", "passwd-", "shadow", "shadow-",
    "group", "group-", "gshadow", "gshadow-",
    "subuid", "subuid-", "subgid", "subgid-",
    ".pwd.lock", NULL,
};

static int starts_with(const char *s, const char *pre, size_t n) {
    return strncmp(s, pre, n) == 0;
}

/* comp 比較 path 一級段（path 形如 "usr/lib"） */
static int top_match(const char *path) {
    for (int i = 0; g_top[i]; i++) {
        const char *t = g_top[i];
        size_t n = strlen(t);
        if (strncmp(path, t, n) == 0 && (path[n] == '\0' || path[n] == '/'))
            return 1;
    }
    return 0;
}

static int etc_keep_match(const char *path) {
    /* path 已知以 "etc/" 開頭。
     * 前綴匹配（v1.3.0）：shadow 工具鏈（groupadd/useradd/passwd）的原子寫
     * 模式是「臨時文件（/etc/group.tmp.xxx、passwd.lock…）+ rename 到正式名」。
     * 若臨時名被重寫到卷側而正式名直通容器層，rename 跨文件系統 EXDEV
     * →「failure while writing changes」。因此 keep 名的任何後綴變體都直通
     * 容器層，讓整條寫入鏈落在同一層；rootfs.go 帳號同步只搬運正式名。 */
    const char *rest = path + 4;
    /* systemd-sysusers 原子寫臨時文件是 glib 風格 .# 前綴（.#groupXXXX/.#passwdXXXX）；
     * 剝掉 .# 後再做前綴匹配，使其與正式名同層容器層，rename 同層成功。 */
    if (rest[0] == '.' && rest[1] == '#')
        rest += 2;
    for (int i = 0; g_etc_keep[i]; i++) {
        const char *t = g_etc_keep[i];
        size_t n = strlen(t);
        if (strncmp(rest, t, n) == 0)
            return 1;
    }
    return 0;
}

/* 核心：把 path 重寫進 buf（長度至少 PATH_MAX+64）。返回要用的路徑指針。 */
static const char *dv_rewrite(const char *path, char *buf) {
    if (!g_enabled || path == NULL || path[0] != '/')
        return path; /* 相對路徑：cwd 已是真實路徑（chdir 已重寫），直通 */

    if (strcmp(path, "/") == 0) {
        /* 裸根：進卷根（VPS 語義） */
        return g_prefix;
    }

    const char *rest = path + 1;
    if (!top_match(rest))
        return path; /* proc/sys/dev/run/tmp/data... 直通 */

    if (starts_with(rest, "etc/", 4) && etc_keep_match(rest))
        return path; /* Docker 管理文件直通 */

    /* /xxx → <prefix>/xxx */
    memcpy(buf, g_prefix, g_prefix_len);
    buf[g_prefix_len] = '/';
    strncpy(buf + g_prefix_len + 1, rest, PATH_MAX);
    return buf;
}

/* 反向：真實路徑 → 虛擬顯示路徑（剝前綴）。僅用於 getcwd/readlink 輸出。 */
static const char *dv_virtualize(const char *real, char *buf, size_t bufsz) {
    if (!g_enabled || real == NULL)
        return real;
    if (strcmp(real, g_prefix) == 0) {
        snprintf(buf, bufsz, "/");
        return buf;
    }
    if (starts_with(real, g_prefix, g_prefix_len) && real[g_prefix_len] == '/') {
        snprintf(buf, bufsz, "%s", real + g_prefix_len);
        return buf;
    }
    return real;
}

/* ---------- 真函數指針（_init 一次初始化） ---------- */

#define DECL(fn) static __typeof__(fn) *real_##fn
DECL(open); DECL(open64); DECL(openat); DECL(openat64);
DECL(creat);
DECL(fopen); DECL(fopen64); DECL(freopen); DECL(freopen64);
DECL(stat); DECL(lstat); DECL(fstatat);
DECL(stat64); DECL(lstat64); DECL(fstatat64); DECL(statx);
DECL(access); DECL(faccessat);
DECL(execve); DECL(execv); DECL(execvp); DECL(execl); DECL(execlp); DECL(execle);
DECL(opendir); DECL(scandir);
DECL(mkdir); DECL(mkdirat); DECL(rmdir);
DECL(unlink); DECL(unlinkat); DECL(remove);
DECL(rename); DECL(renameat); DECL(renameat2);
DECL(symlink); DECL(symlinkat); DECL(link); DECL(linkat);
DECL(readlink); DECL(readlinkat);
DECL(chown); DECL(lchown); DECL(fchownat);
DECL(chmod); DECL(fchmodat);
DECL(truncate);
DECL(utimes); DECL(utime); DECL(lutimes); DECL(utimensat);
DECL(mknod); DECL(mknodat);
DECL(realpath);
DECL(chdir);
DECL(getcwd);
DECL(mkstemp); DECL(mkostemp); DECL(mkstemp64); DECL(mkostemp64);
DECL(mkstemps); DECL(mkostemps);
DECL(mkdtemp);
#undef DECL

/* bookworm glibc 2.36：stat/lstat 為真符號；__xstat 族為 compat，動態程序不再走。
 * 若未來工具鏈需要，可在這裡補 __xstat 攔截。 */

#define LOAD(var, name) do { *(void **)(&(var)) = dlsym(RTLD_NEXT, name); } while (0)

__attribute__((constructor(101)))
static void dv_init(void) {
    /* DATA_ROOT 可配置；未設用 /data */
    const char *root = getenv("DATA_ROOT");
    if (!root || !*root)
        root = "/data";

    /* 前綴長度防呆 */
    int n = snprintf(g_prefix, sizeof(g_prefix), "%s/rootfs", root);
    if (n <= 0 || (size_t)n >= sizeof(g_prefix))
        return;
    g_prefix_len = (size_t)n;

    LOAD(real_open, "open");           LOAD(real_open64, "open64");
    LOAD(real_openat, "openat");       LOAD(real_openat64, "openat64");
    LOAD(real_creat, "creat");
    LOAD(real_fopen, "fopen");         LOAD(real_fopen64, "fopen64");
    LOAD(real_freopen, "freopen");     LOAD(real_freopen64, "freopen64");
    LOAD(real_stat, "stat");           LOAD(real_lstat, "lstat");
    LOAD(real_fstatat, "fstatat");
    LOAD(real_stat64, "stat64");       LOAD(real_lstat64, "lstat64");
    LOAD(real_fstatat64, "fstatat64");
    LOAD(real_statx, "statx");           /* coreutils (ls) & dpkg call statx directly */
    LOAD(real_access, "access");       LOAD(real_faccessat, "faccessat");
    LOAD(real_execve, "execve");       LOAD(real_execv, "execv");
    LOAD(real_execvp, "execvp");       LOAD(real_execl, "execl");
    LOAD(real_execlp, "execlp");       LOAD(real_execle, "execle");
    LOAD(real_opendir, "opendir");     LOAD(real_scandir, "scandir");
    LOAD(real_mkstemp, "mkstemp");     LOAD(real_mkostemp, "mkostemp");
    LOAD(real_mkstemp64, "mkstemp64"); LOAD(real_mkostemp64, "mkostemp64");
    LOAD(real_mkstemps, "mkstemps");   LOAD(real_mkostemps, "mkostemps");
    LOAD(real_mkdtemp, "mkdtemp");
    LOAD(real_mkdir, "mkdir");         LOAD(real_mkdirat, "mkdirat");
    LOAD(real_rmdir, "rmdir");
    LOAD(real_unlink, "unlink");       LOAD(real_unlinkat, "unlinkat");
    LOAD(real_remove, "remove");
    LOAD(real_rename, "rename");       LOAD(real_renameat, "renameat");
    LOAD(real_renameat2, "renameat2");
    LOAD(real_symlink, "symlink");     LOAD(real_symlinkat, "symlinkat");
    LOAD(real_link, "link");           LOAD(real_linkat, "linkat");
    LOAD(real_readlink, "readlink");   LOAD(real_readlinkat, "readlinkat");
    LOAD(real_chown, "chown");         LOAD(real_lchown, "lchown");
    LOAD(real_fchownat, "fchownat");
    LOAD(real_chmod, "chmod");         LOAD(real_fchmodat, "fchmodat");
    LOAD(real_truncate, "truncate");
    LOAD(real_utimes, "utimes");       LOAD(real_utime, "utime");
    LOAD(real_lutimes, "lutimes");     LOAD(real_utimensat, "utimensat");
    LOAD(real_mknod, "mknod");         LOAD(real_mknodat, "mknodat");
    LOAD(real_realpath, "realpath");
    LOAD(real_chdir, "chdir");
    LOAD(real_getcwd, "getcwd");

    /* seed 標記存在才啟用（此時 real_* 已就緒；路徑含 /data 前綴會被 rewrite 直通） */
    if (real_open) {
        char probe[PATH_MAX + 32];
        snprintf(probe, sizeof(probe), "%s/.dvps-seeded", g_prefix);
        int fd = real_open(probe, O_RDONLY);
        if (fd >= 0) {
            close(fd);
            g_enabled = 1;
        }
    }

    /* v1.3.0 兜底：內核 PT_INTERP 直載鏡像層 ld.so，其內部 open（不走 PLT）
     * 只搜鏡像層目錄與 cache → 卷側新裝的 .so（如 libsystemd-shared）找不到。
     * 對每個已啟用攔截的進程補 LD_LIBRARY_PATH（不覆蓋既有值；image ENV 已設
     * 同值，此處是 env 鏈任何斷點時的保底），使 ld.so 的 DT_NEEDED 解析涵蓋
     * 卷側 usr/lib。 */
    if (g_enabled) {
        static char llp[512];
#if defined(__x86_64__)
        const char *archlib = "x86_64-linux-gnu";
#elif defined(__aarch64__)
        const char *archlib = "aarch64-linux-gnu";
#else
        const char *archlib = NULL;
#endif
        int m;
        if (archlib)
            m = snprintf(llp, sizeof(llp), "%s/usr/lib/%s:%s/usr/lib/%s/systemd:%s/usr/lib",
                         g_prefix, archlib, g_prefix, archlib, g_prefix);
        else
            m = snprintf(llp, sizeof(llp), "%s/usr/lib", g_prefix);
        if (m > 0 && (size_t)m < sizeof(llp)) {
            /* bookworm keeps libsystemd-shared-252.so in usr/lib/<t>/systemd/;
             * the two-segment value misses it and ld.so.cache is split
             * (ldconfig writes volume-side, ld.so reads image-side). Upgrade
             * any existing value that lacks the systemd segment; keep one
             * that already has it. */
            const char *cur = getenv("LD_LIBRARY_PATH");
            if (!cur || !strstr(cur, "/systemd"))
                setenv("LD_LIBRARY_PATH", llp, 1);
        }
    }
}

/* ---------- 攔截器 ---------- */

#define RW(p, buf) dv_rewrite((p), (buf))

int open(const char *path, int flags, ...) {
    mode_t mode = 0;
    if (flags & (O_CREAT | O_TMPFILE)) {
        va_list ap; va_start(ap, flags);
        mode = va_arg(ap, mode_t);
        va_end(ap);
    }
    if (!real_open) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_open(RW(path, buf), flags, mode);
}

int open64(const char *path, int flags, ...) {
    mode_t mode = 0;
    if (flags & (O_CREAT | O_TMPFILE)) {
        va_list ap; va_start(ap, flags);
        mode = va_arg(ap, mode_t);
        va_end(ap);
    }
    if (!real_open64) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_open64(RW(path, buf), flags, mode);
}

int openat(int dirfd, const char *path, int flags, ...) {
    mode_t mode = 0;
    if (flags & (O_CREAT | O_TMPFILE)) {
        va_list ap; va_start(ap, flags);
        mode = va_arg(ap, mode_t);
        va_end(ap);
    }
    if (!real_openat) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_openat(dirfd, RW(path, buf), flags, mode);
}

int openat64(int dirfd, const char *path, int flags, ...) {
    mode_t mode = 0;
    if (flags & (O_CREAT | O_TMPFILE)) {
        va_list ap; va_start(ap, flags);
        mode = va_arg(ap, mode_t);
        va_end(ap);
    }
    if (!real_openat64) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_openat64(dirfd, RW(path, buf), flags, mode);
}

int creat(const char *path, mode_t mode) {
    if (!real_creat) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_creat(RW(path, buf), mode);
}

FILE *fopen(const char *path, const char *mode) {
    if (!real_fopen) { errno = ENOSYS; return NULL; }
    char buf[PATH_MAX + 64];
    return real_fopen(RW(path, buf), mode);
}

FILE *fopen64(const char *path, const char *mode) {
    if (!real_fopen64) { errno = ENOSYS; return NULL; }
    char buf[PATH_MAX + 64];
    return real_fopen64(RW(path, buf), mode);
}

FILE *freopen(const char *path, const char *mode, FILE *stream) {
    if (!real_freopen) { errno = ENOSYS; return NULL; }
    char buf[PATH_MAX + 64];
    return real_freopen(RW(path, buf), mode, stream);
}

FILE *freopen64(const char *path, const char *mode, FILE *stream) {
    if (!real_freopen64) { errno = ENOSYS; return NULL; }
    char buf[PATH_MAX + 64];
    return real_freopen64(RW(path, buf), mode, stream);
}

int stat(const char *path, struct stat *st) {
    if (!real_stat) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_stat(RW(path, buf), st);
}

int lstat(const char *path, struct stat *st) {
    if (!real_lstat) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_lstat(RW(path, buf), st);
}

int fstatat(int dirfd, const char *path, struct stat *st, int flags) {
    if (!real_fstatat) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_fstatat(dirfd, RW(path, buf), st, flags);
}

int stat64(const char *path, struct stat64 *st) {
    if (!real_stat64) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_stat64(RW(path, buf), st);
}

int lstat64(const char *path, struct stat64 *st) {
    if (!real_lstat64) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_lstat64(RW(path, buf), st);
}

int fstatat64(int dirfd, const char *path, struct stat64 *st, int flags) {
    if (!real_fstatat64) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_fstatat64(dirfd, RW(path, buf), st, flags);
}

int statx(int dirfd, const char *path, int flags, unsigned int mask, struct statx *stx) {
    if (!real_statx) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_statx(dirfd, RW(path, buf), flags, mask, stx);
}

/* exec 族：glibc 內部（execvp 的 PATH 搜索、execl 的 vararg 展開）會以內部符號
 * 直調 execve 繞過 PLT 攔截——候選虛擬路徑直達鏡像層副本會 ENOENT（後裝命令）。
 * 故這些包裝在此重新實現：展開/搜索後一律走 real_execve(重寫後路徑)。 */

extern char **environ;

int execv(const char *path, char *const argv[]) {
    if (!real_execve) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_execve(RW(path, buf), argv, environ);
}

/* 逐 PATH 候選嘗試 exec（失敗繼續），語義對齊 execvp/execlp */
static int dv_execvp_impl(const char *file, char *const argv[], char *const envp[]) {
    if (!real_execve) { errno = ENOSYS; return -1; }
    if (strchr(file, '/')) {
        char buf[PATH_MAX + 64];
        return real_execve(RW(file, buf), argv, envp);
    }
    const char *path_env = getenv("PATH");
    if (!path_env || !*path_env)
        path_env = "/bin:/usr/bin";
    char pbuf[PATH_MAX * 2];
    strncpy(pbuf, path_env, sizeof(pbuf) - 1);
    pbuf[sizeof(pbuf) - 1] = '\0';
    char *save = NULL;
    int last_errno = ENOENT;
    for (char *dir = strtok_r(pbuf, ":", &save); dir;
         dir = strtok_r(NULL, ":", &save)) {
        char cand[PATH_MAX + 64];
        snprintf(cand, sizeof(cand), "%s/%s", dir, file);
        char buf[PATH_MAX + 64];
        real_execve(RW(cand, buf), argv, envp); /* 成功不返回 */
        if (errno != ENOENT && errno != EACCES) {
            last_errno = errno;
            break;
        }
    }
    errno = last_errno;
    return -1;
}

int execvp(const char *file, char *const argv[]) {
    return dv_execvp_impl(file, argv, environ);
}

int execl(const char *path, const char *arg0, ...) {
    if (!real_execve) { errno = ENOSYS; return -1; }
    char *argv_stack[256];
    va_list ap; va_start(ap, arg0);
    argv_stack[0] = (char *)arg0;
    int i = 1;
    for (; i < 255; i++) {
        char *a = va_arg(ap, char *);
        argv_stack[i] = a;
        if (!a) break;
    }
    if (i == 255) argv_stack[i] = NULL;
    va_end(ap);
    char buf[PATH_MAX + 64];
    return real_execve(RW(path, buf), argv_stack, environ);
}

int execlp(const char *file, const char *arg0, ...) {
    char *argv_stack[256];
    va_list ap; va_start(ap, arg0);
    argv_stack[0] = (char *)arg0;
    int i = 1;
    for (; i < 255; i++) {
        char *a = va_arg(ap, char *);
        argv_stack[i] = a;
        if (!a) break;
    }
    if (i == 255) argv_stack[i] = NULL;
    va_end(ap);
    return dv_execvp_impl(file, argv_stack, environ);
}

int execle(const char *path, const char *arg0, ...) {
    if (!real_execve) { errno = ENOSYS; return -1; }
    char *argv_stack[256];
    char **envp = NULL;
    va_list ap; va_start(ap, arg0);
    argv_stack[0] = (char *)arg0;
    int i = 1;
    for (; i < 255; i++) {
        char *a = va_arg(ap, char *);
        argv_stack[i] = a;
        if (!a) {                 /* NULL 終止 argv；其後緊跟 envp */
            envp = va_arg(ap, char **);
            break;
        }
    }
    if (i == 255) argv_stack[i] = NULL;
    va_end(ap);
    if (!envp) envp = environ;
    char buf[PATH_MAX + 64];
    return real_execve(RW(path, buf), argv_stack, envp);
}

/* mkstemp 族：template 為 in-out 可寫緩衝（apt 下載線程在
 * /var/lib/apt/lists/partial 建 .XXXXXX 臨時文件）。不攔截時寫入直達
 * 容器根，而攔截層的 mkdir 又把父目錄建在了卷側 → mkstemp ENOENT。
 *
 * 陷阱（開發期實錘）：不可把「帶前綴路徑」memcpy 回調用方緩衝——apt
 * 按 template 原長度分配堆內存，前綴 +12B 直接寫回 = 堆溢出（glibc
 * "corrupted size vs. prev_size" + SIGABRT）。
 *
 * 正解：在重寫後（卷側）路徑上創建臨時文件，再把「剝掉前綴的虛擬
 * 路徑」寫回 template。mkstemp 只把 XXXXXX 等長替換為隨機字符，故
 * 虛擬路徑與原 template 嚴格等長（前綴 +12 與剝前綴 -12 相消）→
 * 零溢出。mkstemp(t) ≡ mkostemps(t, 0, 0) 統一走 mkostemps。 */
static int dv_mkstemp_impl(char *tmpl, int suffixlen, int flags) {
    if (!tmpl) { errno = EFAULT; return -1; }
    if (!real_mkostemps) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    const char *np = RW(tmpl, buf);
    if (np == tmpl) /* 白名單外（/tmp 等）或相對路徑：直通 */
        return real_mkostemps(tmpl, suffixlen, flags);
    int fd = real_mkostemps(buf, suffixlen, flags);
    if (fd >= 0) {
        char vbuf[PATH_MAX + 64];
        const char *vp = dv_virtualize(buf, vbuf, sizeof(vbuf));
        memcpy(tmpl, vp, strlen(vp) + 1); /* 與原 template 等長 */
    }
    return fd;
}

int mkstemp(char *tmpl)                       { return dv_mkstemp_impl(tmpl, 0, 0); }
int mkostemp(char *tmpl, int flags)           { return dv_mkstemp_impl(tmpl, 0, flags); }
int mkstemp64(char *tmpl)                     { return dv_mkstemp_impl(tmpl, 0, 0); }
int mkostemp64(char *tmpl, int flags)         { return dv_mkstemp_impl(tmpl, 0, flags); }
int mkstemps(char *tmpl, int suffixlen)       { return dv_mkstemp_impl(tmpl, suffixlen, 0); }
int mkostemps(char *tmpl, int suffixlen, int flags) { return dv_mkstemp_impl(tmpl, suffixlen, flags); }

char *mkdtemp(char *tmpl) {
    if (!tmpl) { errno = EFAULT; return NULL; }
    if (!real_mkdtemp) { errno = ENOSYS; return NULL; }
    char buf[PATH_MAX + 64];
    const char *np = RW(tmpl, buf);
    if (np == tmpl) return real_mkdtemp(tmpl);
    if (real_mkdtemp(buf) == NULL) return NULL;
    char vbuf[PATH_MAX + 64];
    const char *vp = dv_virtualize(buf, vbuf, sizeof(vbuf));
    memcpy(tmpl, vp, strlen(vp) + 1); /* 與原 template 等長 */
    return tmpl;
}

int access(const char *path, int mode) {
    if (!real_access) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_access(RW(path, buf), mode);
}

int faccessat(int dirfd, const char *path, int mode, int flags) {
    if (!real_faccessat) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_faccessat(dirfd, RW(path, buf), mode, flags);
}

int execve(const char *path, char *const argv[], char *const envp[]) {
    if (!real_execve) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_execve(RW(path, buf), argv, envp);
}

DIR *opendir(const char *path) {
    if (!real_opendir) { errno = ENOSYS; return NULL; }
    char buf[PATH_MAX + 64];
    return real_opendir(RW(path, buf));
}

int scandir(const char *path, struct dirent ***namelist,
            int (*sel)(const struct dirent *),
            int (*cmp)(const struct dirent **, const struct dirent **)) {
    if (!real_scandir) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_scandir(RW(path, buf), namelist, sel, cmp);
}

int mkdir(const char *path, mode_t mode) {
    if (!real_mkdir) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_mkdir(RW(path, buf), mode);
}

int mkdirat(int dirfd, const char *path, mode_t mode) {
    if (!real_mkdirat) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_mkdirat(dirfd, RW(path, buf), mode);
}

int rmdir(const char *path) {
    if (!real_rmdir) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_rmdir(RW(path, buf));
}

int unlink(const char *path) {
    if (!real_unlink) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_unlink(RW(path, buf));
}

int unlinkat(int dirfd, const char *path, int flags) {
    if (!real_unlinkat) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_unlinkat(dirfd, RW(path, buf), flags);
}

int remove(const char *path) {
    if (!real_remove) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_remove(RW(path, buf));
}

int rename(const char *old, const char *new_) {
    if (!real_rename) { errno = ENOSYS; return -1; }
    char b1[PATH_MAX + 64], b2[PATH_MAX + 64];
    return real_rename(RW(old, b1), RW(new_, b2));
}

int renameat(int oldfd, const char *old, int newfd, const char *new_) {
    if (!real_renameat) { errno = ENOSYS; return -1; }
    char b1[PATH_MAX + 64], b2[PATH_MAX + 64];
    return real_renameat(oldfd, RW(old, b1), newfd, RW(new_, b2));
}

int renameat2(int oldfd, const char *old, int newfd, const char *new_, unsigned flags) {
    if (!real_renameat2) { errno = ENOSYS; return -1; }
    char b1[PATH_MAX + 64], b2[PATH_MAX + 64];
    return real_renameat2(oldfd, RW(old, b1), newfd, RW(new_, b2), flags);
}

int symlink(const char *target, const char *linkpath) {
    if (!real_symlink) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64], tb[PATH_MAX + 64];
    /* Rewrite the symlink TARGET too: the kernel resolves targets WITHOUT our
     * preload, so a virtual absolute target (apt's staging fallback e.g.
     * /var/cache/apt/archives/x.deb) would dangle in the container layer.
     * Store the REAL path; readlink's dv_virtualize turns it back on the way
     * out, so programs never see the difference. */
    return real_symlink(RW(target, tb), RW(linkpath, buf));
}

int symlinkat(const char *target, int newdirfd, const char *linkpath) {
    if (!real_symlinkat) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64], tb[PATH_MAX + 64];
    /* same as symlink(): the kernel resolves the target itself */
    return real_symlinkat(RW(target, tb), newdirfd, RW(linkpath, buf));
}

int link(const char *old, const char *new_) {
    if (!real_link || !real_symlink) { errno = ENOSYS; return -1; }
    char b1[PATH_MAX + 64], b2[PATH_MAX + 64];
    const char *op = RW(old, b1);
    const char *np = RW(new_, b2);
    int rc = real_link(op, np);
    if (rc == -1 && errno == EXDEV && op[0] == '/') {
        /* Cross-device hardlink (volume <-> container layer, e.g. apt linking
         * a cached .deb into its /tmp staging dir). Fall back to a symlink
         * pointing at the REAL path: the kernel resolves symlink targets
         * WITHOUT our preload, so the target must be the bind-mount-visible
         * real location (/data/rootfs/...), never the virtual one. */
        if (real_symlink(op, np) == 0) return 0;
        errno = EXDEV;
        return -1;
    }
    return rc;
}

int linkat(int oldfd, const char *old, int newfd, const char *new_, int flags) {
    if (!real_linkat || !real_symlinkat) { errno = ENOSYS; return -1; }
    char b1[PATH_MAX + 64], b2[PATH_MAX + 64], real_old[PATH_MAX + 64];
    const char *op = RW(old, b1);
    const char *np = RW(new_, b2);
    int rc = real_linkat(oldfd, op, newfd, np, flags);
    if (rc == -1 && errno == EXDEV) {
        /* Same cross-device fallback as link(). apt's staging pattern uses
         * linkat(AT_FDCWD, "/proc/self/fd/N", ..., AT_SYMLINK_FOLLOW) on an
         * O_TMPFILE fd that lives on the volume: resolve the kernel-truth
         * location of that fd so the symlink target is resolvable without us. */
        const char *target = NULL;
        if (op[0] == '/' && strncmp(op, "/proc/", 6) == 0 && real_readlink) {
            ssize_t r = real_readlink(op, real_old, PATH_MAX);
            if (r > 0) { real_old[r] = '\0'; target = real_old; }
        } else if (op[0] == '/') {
            target = op;
        }
        if (target && real_symlinkat(target, newfd, np) == 0) return 0;
        errno = EXDEV;
        return -1;
    }
    return rc;
}

ssize_t readlink(const char *path, char *out, size_t bufsz) {
    if (!real_readlink) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    ssize_t r = real_readlink(RW(path, buf), out, bufsz);
    if (r > 0) {
        /* 輸出若指向真實前綴，虛擬化顯示 */
        out[r] = '\0';
        char vbuf[PATH_MAX + 64];
        const char *v = dv_virtualize(out, vbuf, sizeof(vbuf));
        if (v != out) {
            size_t vl = strlen(v);
            if (vl < bufsz) { memcpy(out, v, vl + 1); r = (ssize_t)vl; }
        }
    }
    return r;
}

ssize_t readlinkat(int dirfd, const char *path, char *out, size_t bufsz) {
    if (!real_readlinkat) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    ssize_t r = real_readlinkat(dirfd, RW(path, buf), out, bufsz);
    if (r > 0) {
        out[r] = '\0';
        char vbuf[PATH_MAX + 64];
        const char *v = dv_virtualize(out, vbuf, sizeof(vbuf));
        if (v != out) {
            size_t vl = strlen(v);
            if (vl < bufsz) { memcpy(out, v, vl + 1); r = (ssize_t)vl; }
        }
    }
    return r;
}

int chown(const char *path, uid_t owner, gid_t group) {
    if (!real_chown) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_chown(RW(path, buf), owner, group);
}

int lchown(const char *path, uid_t owner, gid_t group) {
    if (!real_lchown) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_lchown(RW(path, buf), owner, group);
}

int fchownat(int dirfd, const char *path, uid_t owner, gid_t group, int flags) {
    if (!real_fchownat) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_fchownat(dirfd, RW(path, buf), owner, group, flags);
}

int chmod(const char *path, mode_t mode) {
    if (!real_chmod) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_chmod(RW(path, buf), mode);
}

int fchmodat(int dirfd, const char *path, mode_t mode, int flags) {
    if (!real_fchmodat) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_fchmodat(dirfd, RW(path, buf), mode, flags);
}

int truncate(const char *path, off_t length) {
    if (!real_truncate) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_truncate(RW(path, buf), length);
}

int utimes(const char *path, const struct timeval times[2]) {
    if (!real_utimes) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_utimes(RW(path, buf), times);
}

int utime(const char *path, const struct utimbuf *times) {
    if (!real_utime) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_utime(RW(path, buf), times);
}

int lutimes(const char *path, const struct timeval times[2]) {
    if (!real_lutimes) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_lutimes(RW(path, buf), times);
}

int utimensat(int dirfd, const char *path, const struct timespec times[2], int flags) {
    if (!real_utimensat) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_utimensat(dirfd, RW(path, buf), times, flags);
}

int mknod(const char *path, mode_t mode, dev_t dev) {
    if (!real_mknod) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_mknod(RW(path, buf), mode, dev);
}

int mknodat(int dirfd, const char *path, mode_t mode, dev_t dev) {
    if (!real_mknodat) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_mknodat(dirfd, RW(path, buf), mode, dev);
}

char *realpath(const char *path, char *resolved) {
    if (!real_realpath) { errno = ENOSYS; return NULL; }
    char buf[PATH_MAX + 64];
    char stackbuf[PATH_MAX];
    char *target = resolved ? resolved : stackbuf;
    char *r = real_realpath(RW(path, buf), target);
    if (r) {
        char vbuf[PATH_MAX + 64];
        const char *v = dv_virtualize(r, vbuf, sizeof(vbuf));
        if (v != r) {
            if (!resolved) {
                /* 呼叫者期望堆分配 */
                char *heap = malloc(strlen(v) + 1);
                if (heap) { strcpy(heap, v); return heap; }
                return r; /* malloc 失敗退回真實路徑 */
            }
            strcpy(r, v); /* resolved 緩衝由呼叫者保證 PATH_MAX */
        }
    }
    return r;
}

int chdir(const char *path) {
    if (!real_chdir) { errno = ENOSYS; return -1; }
    char buf[PATH_MAX + 64];
    return real_chdir(RW(path, buf));
}

char *getcwd(char *buf, size_t size) {
    if (!real_getcwd) { errno = ENOSYS; return NULL; }
    char tmp[PATH_MAX];
    char *r = real_getcwd(tmp, sizeof(tmp));
    if (!r) return NULL;
    char vbuf[PATH_MAX + 64];
    const char *v = dv_virtualize(tmp, vbuf, sizeof(vbuf));
    size_t need = strlen(v) + 1;
    if (!buf) {
        if (size && size < need) { errno = ERANGE; return NULL; }
        char *heap = malloc(need);
        if (!heap) { errno = ENOMEM; return NULL; }
        memcpy(heap, v, need);
        return heap;
    }
    if (size < need) { errno = ERANGE; return NULL; }
    memcpy(buf, v, need);
    return buf;
}
