/* sqlread in C: the same algorithms as ../sqlite/*.ov, function for
** function, so that timing the two compares the compilers and not the
** programs. Keep it in step with the Ovid source. Build with -fwrapv. */
#include <fcntl.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <unistd.h>

typedef int64_t i64;
typedef uint8_t u8;

#include "pow10tab.h"

static i64 load8(i64 p) { return *(u8 *)p; }
static i64 load64(i64 p) { i64 v; memcpy(&v, (void *)p, 8); return v; }
static void store8(i64 p, i64 v) { *(u8 *)p = (u8)v; }
static void store64(i64 p, i64 v) { memcpy((void *)p, &v, 8); }

/* ---- ovid/io, ovid/mem ---- */

static i64 Alloc(i64 n) {
  void *p = calloc(1, n < 8 ? 8 : n);
  if (!p) _exit(125);
  return (i64)p;
}

static i64 Write(i64 fd, i64 p, i64 n) {
  i64 off = 0;
  while (off < n) {
    i64 w = write(fd, (void *)(p + off), n - off);
    if (w <= 0) return -1;
    off = off + w;
  }
  return 0;
}

static i64 CLen(i64 p) {
  i64 i = 0;
  while (load8(p + i) != 0) i = i + 1;
  return i;
}

static i64 ReadFile(i64 path, i64 outp, i64 outn) {
  int fd = open((char *)path, O_RDONLY);
  if (fd < 0) return -1;
  struct stat st;
  if (fstat(fd, &st) < 0) { close(fd); return -1; }
  i64 sz = st.st_size;
  i64 buf = Alloc(sz + 1);
  i64 got = 0;
  while (got < sz) {
    i64 r = read(fd, (void *)(buf + got), sz - got);
    if (r <= 0) { close(fd); return -1; }
    got = got + r;
  }
  close(fd);
  store64(outp, buf);
  store64(outn, sz);
  return 0;
}

static int Eq(i64 a, i64 an, i64 b, i64 bn) {
  if (an != bn) return 0;
  i64 i = 0;
  while (i + 8 <= an) {
    if (load64(a + i) != load64(b + i)) return 0;
    i = i + 8;
  }
  while (i < an) {
    if (load8(a + i) != load8(b + i)) return 0;
    i = i + 1;
  }
  return 1;
}

static i64 Copy(i64 dst, i64 src, i64 n) {
  i64 i = 0;
  if (dst < src || dst - src >= 8) {
    while (i + 8 <= n) {
      store64(dst + i, load64(src + i));
      i = i + 8;
    }
  }
  while (i < n) {
    store8(dst + i, load8(src + i));
    i = i + 1;
  }
  return n;
}

typedef struct { i64 data, len, cap; } Buf;

static Buf *BufNew(i64 cap) {
  Buf *b = (Buf *)Alloc(sizeof(Buf));
  b->data = Alloc(cap);
  b->cap = cap;
  return b;
}

static void Grow(Buf *b, i64 need) {
  if (b->len + need <= b->cap) return;
  i64 nc = b->cap * 2;
  if (nc < 64) nc = 64;
  while (b->len + need > nc) nc = nc * 2;
  i64 nd = Alloc(nc);
  Copy(nd, b->data, b->len);
  b->data = nd;
  b->cap = nc;
}

static void WByte(Buf *b, i64 c) {
  Grow(b, 1);
  store8(b->data + b->len, c);
  b->len = b->len + 1;
}

static void WBytes(Buf *b, i64 p, i64 n) {
  Grow(b, n);
  Copy(b->data + b->len, p, n);
  b->len = b->len + n;
}

/* ---- sqlite/db ---- */

enum { ErrOpen = 1, ErrNotDB, ErrEncoding, ErrCorrupt, ErrNotTable };

typedef struct { i64 data, size, page, usable, npages, wal, err; } DB;
typedef struct { i64 p; } Rd;

static const char *ErrStr(i64 code) {
  if (code == ErrOpen) return "cannot read the file";
  if (code == ErrNotDB) return "not a SQLite 3 database";
  if (code == ErrEncoding) return "text encoding is not UTF-8";
  if (code == ErrNotTable) return "not a rowid table (WITHOUT ROWID and virtual tables are not supported)";
  return "database is malformed";
}

static i64 Be16(i64 p) { return (load8(p) << 8) | load8(p + 1); }
static i64 Be32(i64 p) { return (Be16(p) << 16) | Be16(p + 2); }

static i64 Varint(Rd *r) {
  i64 v = 0;
  i64 i = 0;
  while (i < 8) {
    i64 c = load8(r->p + i);
    v = (v << 7) | (c & 127);
    if (c < 128) {
      r->p = r->p + i + 1;
      return v;
    }
    i = i + 1;
  }
  v = (v << 8) | load8(r->p + 8);
  r->p = r->p + 9;
  return v;
}

static DB *Open(i64 path) {
  DB *db = (DB *)Alloc(sizeof(DB));
  i64 pp = Alloc(8);
  i64 nn = Alloc(8);
  if (ReadFile(path, pp, nn) != 0) { db->err = ErrOpen; return db; }
  db->data = load64(pp);
  db->size = load64(nn);
  if (db->size == 0) return db;
  if (db->size < 100 || !Eq(db->data, 15, (i64)"SQLite format 3", 15) || load8(db->data + 15) != 0) {
    db->err = ErrNotDB;
    return db;
  }
  i64 ps = Be16(db->data + 16);
  if (ps == 1) ps = 65536;
  if (ps < 512 || (ps & (ps - 1)) != 0) { db->err = ErrNotDB; return db; }
  db->page = ps;
  db->usable = ps - load8(db->data + 20);
  db->npages = db->size / ps;
  db->wal = load8(db->data + 18) == 2;
  i64 enc = Be32(db->data + 56);
  if (enc != 1 && enc != 0) db->err = ErrEncoding;
  return db;
}

static i64 pageBase(DB *db, i64 pg) { return db->data + (pg - 1) * db->page; }
static i64 pageHdr(DB *db, i64 pg) {
  if (pg == 1) return db->data + 100;
  return pageBase(db, pg);
}

enum { MaxDepth = 40, PageLeaf = 13, PageInterior = 5 };

typedef struct {
  DB *db;
  Rd *rd;
  i64 stack, depth, rowid, payload, paylen, err;
} Cursor;

static Cursor *NewCursor(DB *db) {
  Cursor *c = (Cursor *)Alloc(sizeof(Cursor));
  c->db = db;
  c->rd = (Rd *)Alloc(sizeof(Rd));
  c->stack = Alloc(MaxDepth * 16);
  c->depth = -1;
  return c;
}

static int fail(Cursor *c, i64 code) {
  c->err = code;
  c->depth = -1;
  return 0;
}

static int push(Cursor *c, i64 pg) {
  if (pg < 1 || pg > c->db->npages || c->depth + 1 >= MaxDepth) return fail(c, ErrCorrupt);
  c->depth = c->depth + 1;
  store64(c->stack + c->depth * 16, pg);
  store64(c->stack + c->depth * 16 + 8, 0);
  return 1;
}

static int First(Cursor *c, i64 root) {
  c->depth = -1;
  c->err = 0;
  if (c->db->npages == 0 && root == 1) return 1;
  return push(c, root);
}

static i64 payloadAt(Cursor *c, i64 p, i64 n) {
  i64 u = c->db->usable;
  i64 x = u - 35;
  if (n <= x) return p;
  i64 m = (u - 12) * 32 / 255 - 23;
  i64 local = m + (n - m) % (u - 4);
  if (local > x) local = m;
  i64 buf = Alloc(n);
  Copy(buf, p, local);
  i64 got = local;
  i64 ovf = Be32(p + local);
  while (got < n && ovf >= 1 && ovf <= c->db->npages) {
    i64 a = pageBase(c->db, ovf);
    i64 take = u - 4;
    if (take > n - got) take = n - got;
    Copy(buf + got, a + 4, take);
    got = got + take;
    ovf = Be32(a);
  }
  if (got < n) c->err = ErrCorrupt;
  return buf;
}

static int leafCell(Cursor *c, i64 cell) {
  c->rd->p = cell;
  c->paylen = Varint(c->rd);
  c->rowid = Varint(c->rd);
  c->payload = payloadAt(c, c->rd->p, c->paylen);
  return c->err == 0;
}

static int Next(Cursor *c) {
  while (c->depth >= 0) {
    i64 slot = c->stack + c->depth * 16;
    i64 pg = load64(slot);
    i64 idx = load64(slot + 8);
    i64 base = pageBase(c->db, pg);
    i64 h = pageHdr(c->db, pg);
    i64 kind = load8(h);
    i64 ncell = Be16(h + 3);
    store64(slot + 8, idx + 1);
    if (kind == PageLeaf) {
      if (idx < ncell) return leafCell(c, base + Be16(h + 8 + idx * 2));
      c->depth = c->depth - 1;
    } else if (kind == PageInterior) {
      if (idx < ncell) {
        if (!push(c, Be32(base + Be16(h + 12 + idx * 2)))) return 0;
      } else if (idx == ncell) {
        if (!push(c, Be32(h + 8))) return 0;
      } else {
        c->depth = c->depth - 1;
      }
    } else {
      return fail(c, ErrNotTable);
    }
  }
  return 0;
}

static int Seek(Cursor *c, i64 root, i64 rowid) {
  c->depth = -1;
  c->err = 0;
  i64 pg = root;
  i64 level = 0;
  while (level < MaxDepth) {
    if (pg < 1 || pg > c->db->npages) return fail(c, ErrCorrupt);
    i64 base = pageBase(c->db, pg);
    i64 h = pageHdr(c->db, pg);
    i64 kind = load8(h);
    i64 ncell = Be16(h + 3);
    i64 i = 0;
    if (kind == PageLeaf) {
      while (i < ncell) {
        i64 cell = base + Be16(h + 8 + i * 2);
        c->rd->p = cell;
        Varint(c->rd);
        i64 k = Varint(c->rd);
        if (k == rowid) return leafCell(c, cell);
        if (k > rowid) return 0;
        i = i + 1;
      }
      return 0;
    }
    if (kind != PageInterior) return fail(c, ErrNotTable);
    pg = Be32(h + 8);
    int found = 0;
    while (i < ncell && !found) {
      i64 icell = base + Be16(h + 12 + i * 2);
      c->rd->p = icell + 4;
      if (rowid <= Varint(c->rd)) {
        pg = Be32(icell);
        found = 1;
      }
      i = i + 1;
    }
    level = level + 1;
  }
  return fail(c, ErrCorrupt);
}

enum { KNull, KInt, KReal, KText, KBlob };

typedef struct {
  Rd *rd;
  i64 hend, body, kind, ival, ptr, len;
} Rec;

static Rec *NewRec(void) {
  Rec *r = (Rec *)Alloc(sizeof(Rec));
  r->rd = (Rd *)Alloc(sizeof(Rd));
  return r;
}

static void RecInit(Rec *r, i64 payload) {
  r->rd->p = payload;
  i64 hs = Varint(r->rd);
  r->hend = payload + hs;
  r->body = payload + hs;
}

static i64 beInt(i64 p, i64 n) {
  i64 v = 0;
  i64 i = 0;
  while (i < n) {
    v = (v << 8) | load8(p + i);
    i = i + 1;
  }
  i64 sh = 64 - n * 8;
  return (v << sh) >> sh;
}

static int RecNext(Rec *r) {
  if (r->rd->p >= r->hend) return 0;
  i64 t = Varint(r->rd);
  i64 n = 0;
  r->kind = KInt;
  r->ival = 0;
  if (t == 0) {
    r->kind = KNull;
  } else if (t <= 4) {
    n = t;
  } else if (t == 5) {
    n = 6;
  } else if (t == 6) {
    n = 8;
  } else if (t == 7) {
    n = 8;
    r->kind = KReal;
  } else if (t == 8) {
    r->ival = 0;
  } else if (t == 9) {
    r->ival = 1;
  } else if (t < 12) {
    r->kind = KNull;
  } else if ((t & 1) == 0) {
    r->kind = KBlob;
    r->len = (t - 12) / 2;
    r->ptr = r->body;
    r->body = r->body + r->len;
    return 1;
  } else {
    r->kind = KText;
    r->len = (t - 13) / 2;
    r->ptr = r->body;
    r->body = r->body + r->len;
    return 1;
  }
  if (n > 0) {
    r->ival = beInt(r->body, n);
    r->body = r->body + n;
  }
  return 1;
}

typedef struct Table {
  i64 name, namen, root, sql, sqln, ipk, ncol, real;
  struct Table *next;
} Table;

static i64 lower(i64 c) {
  if (c >= 65 && c <= 90) return c + 32;
  return c;
}

static int isWord(i64 c) {
  return (c >= 48 && c <= 57) || (c >= 65 && c <= 90) || (c >= 97 && c <= 122) || c == 95 || c >= 128;
}

static int isSpace(i64 c) { return c == 32 || c == 9 || c == 10 || c == 13; }

static int EqFold(i64 a, i64 an, i64 b, i64 bn) {
  if (an != bn) return 0;
  i64 i = 0;
  while (i < an) {
    if (lower(load8(a + i)) != lower(load8(b + i))) return 0;
    i = i + 1;
  }
  return 1;
}

static Table *FindTable(Table *t, i64 name, i64 namen) {
  while (t != 0) {
    if (EqFold(t->name, t->namen, name, namen)) return t;
    t = t->next;
  }
  return t;
}

static i64 skipQuoted(i64 p, i64 n, i64 i) {
  i64 q = load8(p + i);
  if (q == 91) {
    q = 93;
  } else if (q != 34 && q != 39 && q != 96) {
    return i;
  }
  i = i + 1;
  while (i < n && load8(p + i) != q) i = i + 1;
  return i + 1;
}

static int wordAt(i64 p, i64 n, i64 i, const char *lit, i64 litn) {
  if (i + litn > n || !EqFold(p + i, litn, (i64)lit, litn)) return 0;
  return i + litn == n || !isWord(load8(p + i + litn));
}

static i64 afterName(i64 p, i64 n) {
  i64 i = 0;
  while (i < n && isSpace(load8(p + i))) i = i + 1;
  i64 j = skipQuoted(p, n, i);
  if (j == i) {
    while (j < n && isWord(load8(p + j))) j = j + 1;
  }
  while (j < n && isSpace(load8(p + j))) j = j + 1;
  return j;
}

static int isIPK(i64 p, i64 n) {
  i64 i = afterName(p, n);
  if (!wordAt(p, n, i, "integer", 7)) return 0;
  i = i + 7;
  while (i < n) {
    if (wordAt(p, n, i, "primary", 7) && !isWord(load8(p + i - 1))) {
      i = i + 7;
      while (i < n && isSpace(load8(p + i))) i = i + 1;
      return wordAt(p, n, i, "key", 3);
    }
    i = i + 1;
  }
  return 0;
}

static int endsType(i64 p, i64 n, i64 i) {
  if (isWord(load8(p + i - 1))) return 0;
  return wordAt(p, n, i, "constraint", 10) || wordAt(p, n, i, "primary", 7) || wordAt(p, n, i, "not", 3) ||
         wordAt(p, n, i, "null", 4) || wordAt(p, n, i, "unique", 6) || wordAt(p, n, i, "check", 5) ||
         wordAt(p, n, i, "default", 7) || wordAt(p, n, i, "collate", 7) || wordAt(p, n, i, "references", 10) ||
         wordAt(p, n, i, "generated", 9) || wordAt(p, n, i, "as", 2);
}

static int has(i64 p, i64 from, i64 to, const char *lit, i64 litn) {
  while (from + litn <= to) {
    if (EqFold(p + from, litn, (i64)lit, litn)) return 1;
    from = from + 1;
  }
  return 0;
}

static int isReal(i64 p, i64 n) {
  i64 i = afterName(p, n);
  i64 end = i;
  while (end < n && !endsType(p, n, end)) end = end + 1;
  if (has(p, i, end, "int", 3) || has(p, i, end, "char", 4) || has(p, i, end, "clob", 4) ||
      has(p, i, end, "text", 4) || has(p, i, end, "blob", 4))
    return 0;
  return has(p, i, end, "real", 4) || has(p, i, end, "floa", 4) || has(p, i, end, "doub", 4);
}

static void columns(Table *t) {
  i64 sql = t->sql;
  i64 n = t->sqln;
  t->ipk = -1;
  t->real = Alloc(n / 2 + 1);
  i64 i = 0;
  while (i < n && load8(sql + i) != 40) i = i + 1;
  i = i + 1;
  i64 start = i;
  i64 col = 0;
  i64 depth = 0;
  while (i < n) {
    i64 c = load8(sql + i);
    i64 q = skipQuoted(sql, n, i);
    if (q != i) {
      i = q - 1;
    } else if (c == 40) {
      depth = depth + 1;
    } else if (c == 41 && depth > 0) {
      depth = depth - 1;
    } else if ((c == 44 && depth == 0) || c == 41) {
      if (t->ipk < 0 && isIPK(sql + start, i - start)) t->ipk = col;
      if (isReal(sql + start, i - start)) store8(t->real + col, 1);
      col = col + 1;
      start = i + 1;
      if (c == 41) i = n;
    }
    i = i + 1;
  }
  t->ncol = col;
}

static Table *Tables(Cursor *c) {
  Table *head = 0;
  Table *tail = 0;
  Rec *rec = NewRec();
  First(c, 1);
  while (Next(c)) {
    RecInit(rec, c->payload);
    Table *t = (Table *)Alloc(sizeof(Table));
    int isTable = 0;
    i64 col = 0;
    while (RecNext(rec)) {
      if (col == 0) {
        isTable = rec->kind == KText && Eq(rec->ptr, rec->len, (i64)"table", 5);
      } else if (col == 1) {
        t->name = rec->ptr;
        t->namen = rec->len;
      } else if (col == 3) {
        t->root = rec->ival;
      } else if (col == 4 && rec->kind == KText) {
        t->sql = rec->ptr;
        t->sqln = rec->len;
      }
      col = col + 1;
    }
    if (isTable) {
      columns(t);
      if (tail == 0) {
        head = t;
      } else {
        tail->next = t;
      }
      tail = t;
    }
  }
  return head;
}

/* ---- sqlite/real ---- */

enum { Base = 1000000000, Limbs = 100 };

typedef struct { i64 limbs, n; } Big;
typedef struct {
  Big *a, *b, *q;
  i64 digits, lo;
} Fmt;

static Big *newBig(void) {
  Big *b = (Big *)Alloc(sizeof(Big));
  b->limbs = Alloc(Limbs * 8);
  return b;
}

static Fmt *FmtNew(void) {
  Fmt *f = (Fmt *)Alloc(sizeof(Fmt));
  f->a = newBig();
  f->b = newBig();
  f->q = newBig();
  f->digits = Alloc(32);
  return f;
}

static i64 lsr(i64 x, i64 n) {
  if (n == 0) return x;
  return (x >> n) & (((i64)1 << (64 - n)) - 1);
}

static i64 mulhi(i64 a, i64 b) {
  i64 a0 = a & 0xffffffff;
  i64 a1 = lsr(a, 32);
  i64 b0 = b & 0xffffffff;
  i64 b1 = lsr(b, 32);
  i64 a0b1 = a0 * b1;
  i64 a1b0 = a1 * b0;
  i64 t = lsr(a0 * b0, 32) + (a0b1 & 0xffffffff) + (a1b0 & 0xffffffff);
  return a1 * b1 + lsr(a0b1, 32) + lsr(a1b0, 32) + lsr(t, 32);
}

static i64 mul160(Fmt *f, i64 a, i64 alo, i64 b) {
  i64 x2 = lsr(a, 32);
  i64 x1 = a & 0xffffffff;
  i64 y1 = lsr(b, 32);
  i64 y0 = b & 0xffffffff;
  i64 x2y1 = x2 * y1;
  i64 x2y0 = x2 * y0;
  i64 x1y1 = x1 * y1;
  i64 x1y0 = x1 * y0;
  i64 x0y1 = alo * y1;
  i64 r3 = (x2y1 & 0xffffffff) + lsr(x2y0, 32) + lsr(x1y1, 32);
  i64 r2 = (x2y0 & 0xffffffff) + (x1y1 & 0xffffffff) + lsr(x1y0, 32) + lsr(x0y1, 32);
  i64 r1 = (x1y0 & 0xffffffff) + (x0y1 & 0xffffffff) + lsr(alo * y0, 32);
  r2 = r2 + lsr(r1, 32);
  r3 = r3 + lsr(r2, 32);
  f->lo = r2 & 0xffffffff;
  return (lsr(x2y1, 32) << 32) + r3;
}

static i64 pow10hi(Fmt *f, i64 p) {
  i64 g = p / 27;
  i64 n = p % 27;
  if (p < 0) {
    if (p == -1) return (i64)scale[13];
    if (n != 0) {
      g = g - 1;
      n = n + 27;
    }
  } else if (p < 27) {
    return (i64)base[p];
  }
  i64 s = (i64)scale[g + 13];
  if (n == 0) return s;
  i64 x = mul160(f, s, (i64)scalelo[g + 13], (i64)base[n]);
  if (x >= 0) x = (x << 1) | ((f->lo >> 31) & 1);
  return x;
}

static void set(Big *b, i64 v) {
  b->n = 0;
  while (v > 0 || b->n == 0) {
    store64(b->limbs + b->n * 8, v % Base);
    b->n = b->n + 1;
    v = v / Base;
  }
}

static void mul(Big *b, i64 m) {
  i64 carry = 0;
  i64 i = 0;
  while (i < b->n) {
    i64 t = load64(b->limbs + i * 8) * m + carry;
    store64(b->limbs + i * 8, t % Base);
    carry = t / Base;
    i = i + 1;
  }
  if (carry > 0) {
    store64(b->limbs + b->n * 8, carry);
    b->n = b->n + 1;
  }
}

static void pow2(Big *b, i64 k) {
  while (k >= 29) {
    mul(b, 1 << 29);
    k = k - 29;
  }
  if (k > 0) mul(b, (i64)1 << k);
}

static void pow10(Big *b, i64 k) {
  while (k >= 9) {
    mul(b, Base);
    k = k - 9;
  }
  while (k > 0) {
    mul(b, 10);
    k = k - 1;
  }
}

static i64 cmp(Big *a, Big *b) {
  if (a->n != b->n) {
    if (a->n < b->n) return -1;
    return 1;
  }
  i64 i = a->n - 1;
  while (i >= 0) {
    i64 x = load64(a->limbs + i * 8);
    i64 y = load64(b->limbs + i * 8);
    if (x < y) return -1;
    if (x > y) return 1;
    i = i - 1;
  }
  return 0;
}

static void sub(Big *a, Big *b) {
  i64 borrow = 0;
  i64 i = 0;
  while (i < a->n) {
    i64 t = load64(a->limbs + i * 8) - borrow;
    if (i < b->n) t = t - load64(b->limbs + i * 8);
    borrow = 0;
    if (t < 0) {
      t = t + Base;
      borrow = 1;
    }
    store64(a->limbs + i * 8, t);
    i = i + 1;
  }
  while (a->n > 1 && load64(a->limbs + (a->n - 1) * 8) == 0) a->n = a->n - 1;
}

static i64 lit(i64 dst, i64 o, const char *s) {
  i64 i = 0;
  while (s[i] != 0) {
    store8(dst + o + i, s[i]);
    i = i + 1;
  }
  return o + i;
}

static int roundTrips(Fmt *f, i64 m, i64 e2, i64 v, i64 e10) {
  i64 g = e2 - 2;
  set(f->a, m * 4);
  set(f->b, v);
  set(f->q, 1);
  if (g > 0) {
    pow2(f->a, g);
    pow2(f->q, g);
  } else {
    pow2(f->b, 0 - g);
  }
  if (e10 < 0) {
    pow10(f->a, 0 - e10);
    pow10(f->q, 0 - e10);
  } else {
    pow10(f->b, e10);
  }
  i64 c = cmp(f->a, f->b);
  if (c == 0) return 1;
  Big *diff = f->a;
  if (c > 0) {
    sub(f->a, f->b);
  } else {
    sub(f->b, f->a);
    diff = f->b;
  }
  if (!(c > 0 && m == ((i64)1 << 52) && e2 > -1074)) mul(f->q, 2);
  c = cmp(diff, f->q);
  if (c == 0) return (m & 1) == 0;
  return c < 0;
}

static i64 prefix(Fmt *f, i64 n) {
  i64 v = 0;
  i64 i = 0;
  while (i < n) {
    v = v * 10 + (load8(f->digits + i) - 48);
    i = i + 1;
  }
  return v;
}

static i64 roundAt(Fmt *f, i64 n) {
  i64 d = f->digits;
  if (load8(d + n) < 53) return 0;
  i64 i = n - 1;
  while (i >= 0 && load8(d + i) == 57) {
    store8(d + i, 48);
    i = i - 1;
  }
  if (i >= 0) {
    store8(d + i, load8(d + i) + 1);
    return 0;
  }
  store8(d, 49);
  return 1;
}

static i64 Format(Fmt *f, i64 dst, i64 bits) {
  i64 exp = (bits >> 52) & 0x7ff;
  i64 m = bits & 0xfffffffffffff;
  i64 o = 0;
  if (exp == 0x7ff) {
    if (m != 0) return lit(dst, 0, "NaN");
    if (bits < 0) return lit(dst, 0, "-Inf");
    return lit(dst, 0, "Inf");
  }
  i64 e = -1074;
  if (exp != 0) {
    m = m | ((i64)1 << 52);
    e = exp - 1075;
  }
  if (m == 0) return lit(dst, 0, "0.0");
  if (bits < 0) {
    store8(dst, 45);
    o = 1;
  }
  i64 v = m;
  i64 ve = e;
  while (v > 0) {
    v = v << 1;
    ve = ve - 1;
  }
  i64 p = 17 - (((ve + 63) * 78913) >> 18);
  i64 h = lsr(mulhi(v, pow10hi(f, p)), 0 - (ve + ((p * 108853) >> 15) + 2));
  i64 dec = lsr(h + ((h << 1) & 2), 1);
  i64 n = 18;
  if (dec >= 1000000000000000000) n = 19;
  i64 d = f->digits;
  i64 i = n - 1;
  while (i >= 0) {
    store8(d + i, 48 + dec % 10);
    dec = dec / 10;
    i = i - 1;
  }
  i64 x = n - p - 1;
  i64 cut = 17;
  i64 jj = 14;
  if (load8(d + 15) == 57 && load8(d + 14) == 57) {
    while (jj > 0 && load8(d + jj - 1) == 57) jj = jj - 1;
    i64 up = 1;
    if (jj > 0) up = prefix(f, jj) + 1;
    if (roundTrips(f, m, e, up, x + 1 - jj)) cut = jj + 1;
  } else if (x + 1 >= n || (load8(d + 15) == 48 && load8(d + 14) == 48 && load8(d + 13) == 48)) {
    jj = 13;
    while (load8(d + jj - 1) == 48) jj = jj - 1;
    if (roundTrips(f, m, e, prefix(f, jj), x + 1 - jj)) cut = jj + 1;
  }
  x = x + roundAt(f, cut);
  i64 nd = cut;
  while (nd > 1 && load8(d + nd - 1) == 48) nd = nd - 1;
  if (x < -4 || x > 16) {
    store8(dst + o, load8(d));
    store8(dst + o + 1, 46);
    o = o + 2;
    if (nd == 1) {
      store8(dst + o, 48);
      o = o + 1;
    }
    i = 1;
    while (i < nd) {
      store8(dst + o, load8(d + i));
      o = o + 1;
      i = i + 1;
    }
    store8(dst + o, 101);
    store8(dst + o + 1, 43);
    if (x < 0) {
      store8(dst + o + 1, 45);
      x = 0 - x;
    }
    o = o + 2;
    if (x >= 100) {
      store8(dst + o, 48 + x / 100);
      o = o + 1;
    }
    store8(dst + o, 48 + (x / 10) % 10);
    store8(dst + o + 1, 48 + x % 10);
    return o + 2;
  }
  if (x < 0) {
    o = lit(dst, o, "0.");
    while (x < -1) {
      store8(dst + o, 48);
      o = o + 1;
      x = x + 1;
    }
    i = 0;
    while (i < nd) {
      store8(dst + o, load8(d + i));
      o = o + 1;
      i = i + 1;
    }
    return o;
  }
  i = 0;
  while (i <= x || i < nd) {
    if (i == x + 1) {
      store8(dst + o, 46);
      o = o + 1;
    }
    if (i < nd) {
      store8(dst + o, load8(d + i));
    } else {
      store8(dst + o, 48);
    }
    o = o + 1;
    i = i + 1;
  }
  if (nd <= x + 1) o = lit(dst, o, ".0");
  return o;
}

static i64 FormatInt(Fmt *f, i64 dst, i64 v) {
  i64 a = v;
  if (v < 0) a = 0 - v;
  if (a == 0) return lit(dst, 0, "0.0");
  i64 top = 62;
  while ((a >> top) == 0) top = top - 1;
  i64 frac = a;
  if (top > 52) {
    frac = a >> (top - 52);
  } else {
    frac = a << (52 - top);
  }
  i64 bits = ((1023 + top) << 52) | (frac & 0xfffffffffffff);
  if (v < 0) bits = bits | ((i64)1 << 63);
  return Format(f, dst, bits);
}

/* ---- sqlite/cli ---- */

static i64 die(const char *msg) {
  Write(2, (i64)"sqlread: ", 9);
  Write(2, (i64)msg, CLen((i64)msg));
  Write(2, (i64)"\n", 1);
  return 1;
}

static void wInt(Buf *out, i64 v) {
  Grow(out, 24);
  i64 end = out->data + out->len + 24;
  i64 i = 0;
  i64 x = v;
  if (x > 0) x = 0 - x;
  while (x != 0 || i == 0) {
    i = i + 1;
    store8(end - i, 48 - x % 10);
    x = x / 10;
  }
  if (v < 0) {
    i = i + 1;
    store8(end - i, 45);
  }
  Copy(out->data + out->len, end - i, i);
  out->len = out->len + i;
}

static i64 parseInt(i64 p, i64 ok) {
  i64 i = 0;
  int neg = load8(p) == 45;
  if (neg) i = 1;
  i64 v = 0;
  i64 digits = 0;
  while (load8(p + i) >= 48 && load8(p + i) <= 57) {
    v = v * 10 + (load8(p + i) - 48);
    i = i + 1;
    digits = digits + 1;
  }
  if (digits > 0 && load8(p + i) == 0) store64(ok, 1);
  if (neg) return 0 - v;
  return v;
}

static void row(Buf *out, Cursor *c, Rec *rec, Table *t, Fmt *f) {
  RecInit(rec, c->payload);
  i64 col = 0;
  while (RecNext(rec)) {
    if (col > 0) WByte(out, 124);
    if (rec->kind == KInt && col < t->ncol && load8(t->real + col) == 1) {
      Grow(out, 40);
      out->len = out->len + FormatInt(f, out->data + out->len, rec->ival);
    } else if (rec->kind == KInt) {
      wInt(out, rec->ival);
    } else if (rec->kind == KReal) {
      Grow(out, 40);
      out->len = out->len + Format(f, out->data + out->len, rec->ival);
    } else if (rec->kind == KNull) {
      if (col == t->ipk) wInt(out, c->rowid);
    } else {
      WBytes(out, rec->ptr, rec->len);
    }
    col = col + 1;
  }
  WByte(out, 10);
  if (out->len >= 65536) {
    Write(1, out->data, out->len);
    out->len = 0;
  }
}

int main(int argc, char **argv) {
  if (argc < 2 || argc > 4) {
    Write(2, (i64)"usage: sqlread file.db [table [rowid | --count]]\n", 49);
    return 64;
  }
  DB *d = Open((i64)argv[1]);
  if (d->err != 0) return die(ErrStr(d->err));
  if (d->wal) die("warning: WAL mode; rows still in the -wal file are not shown");
  Cursor *c = NewCursor(d);
  Table *t = Tables(c);
  if (c->err != 0) return die(ErrStr(c->err));
  Buf *out = BufNew(131072);
  if (argc == 2) {
    while (t != 0) {
      WBytes(out, t->name, t->namen);
      WByte(out, 10);
      t = t->next;
    }
    Write(1, out->data, out->len);
    return 0;
  }
  t = FindTable(t, (i64)argv[2], CLen((i64)argv[2]));
  if (t == 0) return die("no such table");
  Rec *rec = NewRec();
  Fmt *f = FmtNew();
  if (argc == 4 && strcmp(argv[3], "--count") == 0) {
    i64 rows = 0;
    i64 cols = 0;
    First(c, t->root);
    while (Next(c)) {
      RecInit(rec, c->payload);
      while (RecNext(rec)) cols = cols + 1;
      rows = rows + 1;
    }
    wInt(out, rows);
    WByte(out, 124);
    wInt(out, cols);
    WByte(out, 10);
  } else if (argc == 4) {
    i64 ok = Alloc(8);
    i64 id = parseInt((i64)argv[3], ok);
    if (load64(ok) == 0) return die("rowid must be an integer");
    if (Seek(c, t->root, id)) row(out, c, rec, t, f);
  } else {
    First(c, t->root);
    while (Next(c)) row(out, c, rec, t, f);
  }
  Write(1, out->data, out->len);
  if (c->err != 0) return die(ErrStr(c->err));
  return 0;
}
