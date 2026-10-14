// The host side of an Ovid wasm module (ovid build --target wasm; see
// wasm.go), used by examples/workers and by TestCorpusWasm: the
// module's one import, env.syscall(n, a1..a6), answered for the handful of
// Linux system calls ovid/io makes. Standard input is a byte array given
// up front, standard output is collected, and the heap is linear memory
// grown on mmap. Nothing touches a real file system or network.

const SYS = { READ: 0, WRITE: 1, MMAP: 9, MUNMAP: 11, MADVISE: 28, GETPID: 39, EXIT: 60 };
const ENOSYS = -38n, ENOMEM = -12n, EBADF = -9n;

class Exit extends Error {
  constructor(code) {
    super(`exit ${code}`);
    this.code = code;
  }
}

// run instantiates module (a WebAssembly.Module) afresh, so no request sees
// another's memory, feeds it stdin and args (strings), and returns its exit
// code and output.
export function run(module, stdin, args = []) {
  let memory;
  let inPos = 0;
  const out = [], err = [], freed = [];
  const bytes = () => new Uint8Array(memory.buffer);
  const syscall = (n, a, b, c) => {
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
        if (a === 1n) out.push(chunk);
        else if (a === 2n) err.push(chunk);
        else return EBADF;
        return c;
      }
      case SYS.MMAP: {
        // A range munmap gave back is reused, zeroed, before memory grows.
        const pages = Math.ceil(Number(b) / 65536);
        const i = freed.findIndex((f) => f.pages === pages);
        if (i >= 0) {
          const [f] = freed.splice(i, 1);
          bytes().fill(0, f.addr, f.addr + pages * 65536);
          return BigInt(f.addr);
        }
        try {
          return BigInt(memory.grow(pages) * 65536);
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
      default:
        return ENOSYS;
    }
  };
  const inst = new WebAssembly.Instance(module, { env: { syscall } });
  memory = inst.exports.memory;
  let argv = 0;
  if (args.length > 0) {
    const strs = args.map((a) => new TextEncoder().encode(a + "\0"));
    const need = 8 * args.length + strs.reduce((s, b) => s + b.length, 0);
    argv = memory.grow(Math.ceil(need / 65536)) * 65536;
    const view = new DataView(memory.buffer);
    let p = argv + 8 * args.length;
    strs.forEach((b, i) => {
      view.setBigUint64(argv + 8 * i, BigInt(p), true);
      bytes().set(b, p);
      p += b.length;
    });
  }
  let code;
  try {
    code = Number(inst.exports._start(BigInt(args.length), BigInt(argv)));
  } catch (e) {
    if (!(e instanceof Exit)) throw e;
    code = e.code;
  }
  return { code, stdout: concat(out), stderr: concat(err) };
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
