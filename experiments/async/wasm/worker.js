// The default Worker for the async experiments: each request gets a fresh
// instance of app.wasm under runAsync (JSPI), with the request as its
// standard input. bench-wasm.sh copies host.mjs and the module's app.wasm
// next to it; a POC that needs other host ops brings its own worker.js.
import app from "./app.wasm";
import { runAsync, toRaw, fromRaw } from "./host.mjs";

export default {
  async fetch(request) {
    const r = await runAsync(app, await toRaw(request));
    if (r.stderr.length > 0) console.error(new TextDecoder().decode(r.stderr));
    if (r.code !== 0 && r.stdout.length === 0) {
      return new Response(`ovid program exited ${r.code}\n`, { status: 502 });
    }
    return fromRaw(r.stdout);
  },
};
