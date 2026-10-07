// The host side of an Ovid wasm module (ovid build --target wasm; see
// wasm.go), used by examples/workers and by TestCorpusWasm: the
// module's one import, env.syscall(n, a1..a6), answered for the handful of
// Linux system calls ovid/io makes. Standard input is a byte array given
// up front, standard output is collected, and the heap is linear memory
// grown on mmap. Nothing touches a real file system or network.

const SYS = { READ: 0, WRITE: 1, MMAP: 9, MUNMAP: 11, MADVISE: 28, GETPID: 39, EXIT: 60 };
const ENOSYS = -38n, ENOMEM = -12n, EBADF = -9n;

// Host ops: syscall numbers no kernel uses, which only this host answers
// (std/ovid/io/host.ov names them). The ones that wait need runAsync.
//   HOST_SLEEP(ms)        wait ms
//   HOST_SUBMIT(ms, tag)  start a timer of ms that completes with tag; 0 at once
//   HOST_POLL(buf, max)   wait until a timer has completed, then write up to
//                         max completed tags (an i64 each) at buf: the count
export const HOST = { SLEEP: 0x10000, SUBMIT: 0x10001, POLL: 0x10002 };

export class Exit extends Error {
  constructor(code) {
    super(`exit ${code}`);
    this.code = code;
  }
}

// run instantiates module (a WebAssembly.Module) afresh, so no request sees
// another's memory, feeds it stdin and args (strings), and returns its exit
// code, its output, and the size its linear memory grew to (memoryBytes).
// No syscall can wait: a host op that would returns ENOSYS. opts.ops adds
// syscalls: {number: (h, a1, ..., a6) => BigInt}.
export function run(module, stdin, args = [], opts = {}) {
  const h = host(stdin, false, opts);
  const inst = new WebAssembly.Instance(module, { env: { syscall: h.syscall } });
  h.memory = inst.exports.memory;
  h.instance = inst;
  const argv = writeArgs(h, args);
  let code;
  try {
    code = Number(inst.exports._start(BigInt(args.length), BigInt(argv)));
  } catch (e) {
    if (!(e instanceof Exit)) throw e;
    code = e.code;
  }
  return { code, stdout: concat(h.out), stderr: concat(h.err), memoryBytes: h.memory.buffer.byteLength };
}

// runAsync is run where a syscall may wait: the import is a JSPI
// WebAssembly.Suspending, so a host op (or an opts.ops handler) that
// returns a Promise suspends the wasm stack until it settles. It resolves
// to what run returns.
export async function runAsync(module, stdin, args = [], opts = {}) {
  const h = host(stdin, true, opts);
  const inst = new WebAssembly.Instance(module, { env: { syscall: new WebAssembly.Suspending(h.syscall) } });
  h.memory = inst.exports.memory;
  h.instance = inst;
  const argv = writeArgs(h, args);
  let code;
  try {
    code = Number(await WebAssembly.promising(inst.exports._start)(BigInt(args.length), BigInt(argv)));
  } catch (e) {
    if (!(e instanceof Exit)) throw e;
    code = e.code;
  }
  return { code, stdout: concat(h.out), stderr: concat(h.err), memoryBytes: h.memory.buffer.byteLength };
}

// host is the state of one instance and its syscall function. h.memory is
// set once the instance exists; h.bytes() views it (take a new view after
// anything that can grow memory).
function host(stdin, canWait, opts) {
  const h = { memory: null, instance: null, out: [], err: [], canWait };
  const bytes = (h.bytes = () => new Uint8Array(h.memory.buffer));
  let inPos = 0;
  const freed = [];
  const done = []; // completed HOST_SUBMIT tags not yet polled
  let wake = null; // resolves the waiting HOST_POLL
  const poll = (buf, max) => {
    const n = Math.min(Number(max), done.length);
    const view = new DataView(h.memory.buffer);
    for (let i = 0; i < n; i++) view.setBigInt64(Number(buf) + 8 * i, done.shift(), true);
    return BigInt(n);
  };
  h.syscall = (n, a, b, c, d, e, f) => {
    const extra = opts.ops && opts.ops[Number(n)];
    if (extra) return extra(h, a, b, c, d, e, f);
    switch (Number(n)) {
      case SYS.READ: {
        if (a !== 0n) return EBADF;
        const k = Math.min(Number(c), stdin.length - inPos);
        bytes().set(stdin.subarray(inPos, inPos + k), Number(b));
        inPos += k;
        return BigInt(k);
      }
      case SYS.WRITE: {
        const chunk = bytes().slice(Number(b), Number(b) + Number(c));
        if (a === 1n) h.out.push(chunk);
        else if (a === 2n) h.err.push(chunk);
        else return EBADF;
        return c;
      }
      case SYS.MMAP: {
        // A range munmap gave back is reused, zeroed, before memory grows.
        const pages = Math.ceil(Number(b) / 65536);
        const i = freed.findIndex((r) => r.pages === pages);
        if (i >= 0) {
          const [r] = freed.splice(i, 1);
          bytes().fill(0, r.addr, r.addr + pages * 65536);
          return BigInt(r.addr);
        }
        try {
          return BigInt(h.memory.grow(pages) * 65536);
        } catch {
          return ENOMEM;
        }
      }
      case SYS.MUNMAP:
        // wasm memory cannot shrink: the range waits for the next mmap.
        freed.push({ addr: Number(a), pages: Math.ceil(Number(b) / 65536) });
        return 0n;
      case SYS.MADVISE:
        // MADV_DONTNEED: the next touch reads zeros, which ResetHeap relies on.
        if (c === 4n) bytes().fill(0, Number(a), Number(a) + Number(b));
        return 0n;
      case SYS.GETPID:
        return 1n;
      case SYS.EXIT:
        throw new Exit(Number(BigInt.asIntN(64, a)));
      case HOST.SLEEP:
        if (!canWait) return ENOSYS;
        return new Promise((r) => setTimeout(() => r(0n), Number(a)));
      case HOST.SUBMIT:
        setTimeout(() => {
          done.push(b);
          if (wake) {
            const w = wake;
            wake = null;
            w();
          }
        }, Number(a));
        return 0n;
      case HOST.POLL:
        if (done.length > 0) return poll(a, b);
        if (!canWait) return ENOSYS;
        return new Promise((r) => {
          wake = () => r(poll(a, b));
        });
      default:
        return ENOSYS;
    }
  };
  return h;
}

// writeArgs puts args into fresh memory as argv (pointers to NUL-terminated
// strings) and returns its address, or 0 for none.
function writeArgs(h, args) {
  if (args.length === 0) return 0;
  const strs = args.map((a) => new TextEncoder().encode(a + "\0"));
  const need = 8 * args.length + strs.reduce((s, b) => s + b.length, 0);
  const argv = h.memory.grow(Math.ceil(need / 65536)) * 65536;
  const view = new DataView(h.memory.buffer);
  let p = argv + 8 * args.length;
  strs.forEach((b, i) => {
    view.setBigUint64(argv + 8 * i, BigInt(p), true);
    h.bytes().set(b, p);
    p += b.length;
  });
  return argv;
}

export function concat(chunks) {
  const n = chunks.reduce((s, c) => s + c.length, 0);
  const b = new Uint8Array(n);
  let o = 0;
  for (const c of chunks) {
    b.set(c, o);
    o += c.length;
  }
  return b;
}

// toRaw is request as the bytes ovid/http's stdio host reads: an HTTP/1.1
// head and the whole body, with Content-Length in place of any framing.
export async function toRaw(request) {
  const url = new URL(request.url);
  const body = new Uint8Array(await request.arrayBuffer());
  let head = `${request.method} ${url.pathname}${url.search} HTTP/1.1\r\n`;
  for (const [k, v] of request.headers) {
    if (k === "content-length" || k === "transfer-encoding") continue;
    head += `${k}: ${v}\r\n`;
  }
  if (body.length > 0) head += `content-length: ${body.length}\r\n`;
  head += "\r\n";
  return concat([new TextEncoder().encode(head), body]);
}

// fromRaw parses the one response the stdio host wrote.
export function fromRaw(raw) {
  let end = -1;
  for (let i = 0; i + 3 < raw.length; i++) {
    if (raw[i] === 13 && raw[i + 1] === 10 && raw[i + 2] === 13 && raw[i + 3] === 10) {
      end = i;
      break;
    }
  }
  if (end < 0) return new Response("bad gateway: no response head\n", { status: 502 });
  const lines = new TextDecoder().decode(raw.subarray(0, end)).split("\r\n");
  const status = Number(lines[0].split(" ")[1]);
  const headers = new Headers();
  for (const line of lines.slice(1)) {
    const i = line.indexOf(":");
    const k = line.slice(0, i).trim();
    if (k.toLowerCase() === "content-length") continue;
    headers.append(k, line.slice(i + 1).trim());
  }
  const nobody = status === 204 || status === 304 || (status >= 100 && status < 200);
  return new Response(nobody ? null : raw.subarray(end + 4), { status, headers });
}
