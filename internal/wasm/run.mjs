// node run.mjs prog.wasm [args...]: run an Ovid wasm module under host.mjs
// as a command. Standard input is read whole first; the program's stdout
// and stderr are passed through, and its exit code is ours. A trap (a
// failed table bounds check, a division by zero) exits 132, as the native
// ud2's SIGILL makes a shell report.
import { readFileSync, writeSync } from "node:fs";
import { run } from "./host.mjs";

const [file, ...args] = process.argv.slice(2);
const mod = new WebAssembly.Module(readFileSync(file));
let r;
try {
  r = run(mod, new Uint8Array(readFileSync(0)), [file, ...args]);
} catch (e) {
  writeSync(2, `trap: ${e.message}\n`);
  process.exit(132);
}
// writeSync may take part of a buffer, on a pipe.
const writeAll = (fd, b) => {
  for (let o = 0; o < b.length; ) o += writeSync(fd, b, o);
};
writeAll(1, r.stdout);
writeAll(2, r.stderr);
process.exit(r.code & 0xff);
