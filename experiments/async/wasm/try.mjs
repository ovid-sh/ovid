// node --experimental-wasm-jspi try.mjs app.wasm /w1 [/w2 ...]: one request
// per path through runAsync, as the Worker would, without wrangler.
import { readFileSync } from "node:fs";
import { runAsync, toRaw, fromRaw } from "../../../internal/wasm/host.mjs";

const [file, ...paths] = process.argv.slice(2);
const mod = new WebAssembly.Module(readFileSync(file));
let failed = 0;
for (const p of paths) {
  const t = performance.now();
  const r = await runAsync(mod, await toRaw(new Request("http://x" + p)));
  const res = fromRaw(r.stdout);
  console.log(p, "->", r.code, res.status, JSON.stringify(await res.text()), `${(performance.now() - t).toFixed(1)} ms`);
  if (r.stderr.length) console.log("stderr:", new TextDecoder().decode(r.stderr));
  if (r.code !== 0 || res.status !== 200) failed++;
}
process.exit(failed ? 1 : 0);
