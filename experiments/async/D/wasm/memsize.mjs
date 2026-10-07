// node --experimental-wasm-jspi memsize.mjs a.wasm [b.wasm ...]: for each
// module, serve /w1 and /w2 once under runAsync and print the status, the
// body, and how large the instance's linear memory grew.
import { readFileSync } from "node:fs";
import { runAsync, toRaw, fromRaw } from "../../../../internal/wasm/host.mjs";

for (const file of process.argv.slice(2)) {
  const mod = new WebAssembly.Module(readFileSync(file));
  for (const p of ["/w1", "/w2"]) {
    const t = performance.now();
    const r = await runAsync(mod, await toRaw(new Request("http://x" + p)));
    const res = fromRaw(r.stdout);
    const body = await res.text();
    console.log(file.split("/").pop(), p, res.status, JSON.stringify(body), `${(r.memoryBytes / 1048576).toFixed(1)} MiB`, `${(performance.now() - t).toFixed(1)} ms`);
  }
}
