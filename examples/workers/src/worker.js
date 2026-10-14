// A Cloudflare Worker that serves an Ovid program compiled to wasm. Each
// request gets a fresh instance: the request is its standard input, and
// what it writes to standard output is the response.
import hello from "./hello.wasm";
import { run, toRaw, fromRaw } from "../../../internal/wasm/host.mjs";

export default {
  async fetch(request) {
    const r = run(hello, await toRaw(request));
    if (r.stderr.length > 0) console.error(new TextDecoder().decode(r.stderr));
    if (r.code !== 0 && r.stdout.length === 0) {
      return new Response(`ovid program exited ${r.code}\n`, { status: 502 });
    }
    const res = fromRaw(r.stdout);
    res.headers.set("x-powered-by", "ovid (wasm)");
    return res;
  },
};
