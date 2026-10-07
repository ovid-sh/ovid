// D under wasm: experiments/async/wasm/worker.js, plus an x-ovid-memory
// header with the size the instance's linear memory grew to, so the cost
// of each request's heap is visible from the client.
import app from "./app.wasm";
import { runAsync, toRaw, fromRaw } from "./host.mjs";

export default {
  async fetch(request) {
    const r = await runAsync(app, await toRaw(request));
    if (r.stderr.length > 0) console.error(new TextDecoder().decode(r.stderr));
    if (r.code !== 0 && r.stdout.length === 0) {
      return new Response(`ovid program exited ${r.code}\n`, { status: 502 });
    }
    const res = fromRaw(r.stdout);
    res.headers.set("x-ovid-memory", String(r.memoryBytes));
    return res;
  },
};
