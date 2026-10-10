/* The reference: the same output through libsqlite3's C API.
**   api file.db table           every row, columns joined by "|"
**   api file.db table rowid     one row
**   api file.db table --count   "rows|columns", each column fetched
*/
#include <sqlite3.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

static char out[1 << 17];
static size_t len;

static void wr(const char *p, size_t n) {
  size_t off = 0;
  while (off < n) {
    ssize_t w = write(1, p + off, n - off);
    if (w <= 0) exit(1);
    off += w;
  }
}

static void flush(void) {
  wr(out, len);
  len = 0;
}

static void put(const void *p, size_t n) {
  if (len + n > sizeof(out)) flush();
  if (n > sizeof(out)) {
    wr(p, n);
    return;
  }
  memcpy(out + len, p, n);
  len += n;
}

int main(int argc, char **argv) {
  if (argc < 3 || argc > 4) return 64;
  sqlite3 *db;
  if (sqlite3_open_v2(argv[1], &db, SQLITE_OPEN_READONLY, 0) != SQLITE_OK) return 1;
  int count = argc == 4 && strcmp(argv[3], "--count") == 0;
  char *sql = argc == 4 && !count
                  ? sqlite3_mprintf("SELECT * FROM \"%w\" WHERE rowid=%s", argv[2], argv[3])
                  : sqlite3_mprintf("SELECT * FROM \"%w\"", argv[2]);
  sqlite3_stmt *st;
  if (sqlite3_prepare_v2(db, sql, -1, &st, 0) != SQLITE_OK) {
    fprintf(stderr, "api: %s\n", sqlite3_errmsg(db));
    return 1;
  }
  int n = sqlite3_column_count(st);
  long long rows = 0, cols = 0;
  while (sqlite3_step(st) == SQLITE_ROW) {
    rows++;
    for (int i = 0; i < n; i++) {
      if (count) {
        /* Touch the value in its stored type, without converting to text. */
        switch (sqlite3_column_type(st, i)) {
          case SQLITE_INTEGER: sqlite3_column_int64(st, i); break;
          case SQLITE_FLOAT: sqlite3_column_double(st, i); break;
          case SQLITE_NULL: break;
          default: sqlite3_column_blob(st, i); break;
        }
        cols++;
        continue;
      }
      if (i) put("|", 1);
      const void *p = sqlite3_column_blob(st, i);
      if (sqlite3_column_type(st, i) != SQLITE_BLOB) p = sqlite3_column_text(st, i);
      put(p, sqlite3_column_bytes(st, i));
    }
    if (!count) put("\n", 1);
  }
  if (count) len = snprintf(out, sizeof(out), "%lld|%lld\n", rows, cols);
  flush();
  sqlite3_finalize(st);
  sqlite3_close(db);
  return 0;
}
