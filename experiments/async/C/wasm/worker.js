// POC C's Worker: the default one (experiments/async/wasm/worker.js) plus
// the task host ops of tasks.mjs.
import app from "./app.wasm";
import { runAsync, toRaw, fromRaw } from "./host.mjs";
import { taskOps } from "./tasks.mjs";

export default {
  async fetch(request) {
    const r = await runAsync(app, await toRaw(request), [], { ops: taskOps() });
    if (r.stderr.length > 0) console.error(new TextDecoder().decode(r.stderr));
    if (r.code !== 0 && r.stdout.length === 0) {
      return new Response(`ovid program exited ${r.code}\n`, { status: 502 });
    }
    return fromRaw(r.stdout);
  },
};
