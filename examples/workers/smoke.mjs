// node smoke.mjs: run src/hello.wasm under the Worker's host shim, without
// wrangler, against a few requests.
import { readFileSync } from "node:fs";
import { run, toRaw, fromRaw } from "../../internal/wasm/host.mjs";

const mod = new WebAssembly.Module(readFileSync(new URL("./src/hello.wasm", import.meta.url)));
const cases = [
  new Request("http://x/"),
  new Request("http://x/hello?ovid"),
  new Request("http://x/hello", { method: "POST", body: "12345" }),
  new Request("http://x/nope"),
];
let failed = 0;
for (const req of cases) {
  const r = run(mod, await toRaw(req));
  const res = fromRaw(r.stdout);
  console.log(req.method, new URL(req.url).pathname + new URL(req.url).search, "->", r.code, res.status, JSON.stringify(await res.text()));
  if (r.code !== 0) failed++;
}
process.exit(failed ? 1 : 0);
