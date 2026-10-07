// Host ops for POC C's tasks under wasm: each task is one JSPI call of the
// module's _task export (the backend generates it for every taskfn), so
// several can be suspended in one instance at once. Pass taskOps() as
// runAsync's opts.ops; it keeps the tasks of one instance.
//
//   SYS_HOST_SPAWN(fn, cap, arg)  start _task(fn, cap, arg) once the caller
//                                 next waits; a task id. fn 0 spawns
//                                 nothing and answers 0 ("tasks are here").
//   SYS_HOST_JOIN(id)             wait for the task; its result
export const SPAWN = 0x10010;
export const JOIN = 0x10011;

export function taskOps() {
  const tasks = new Map();
  let next = 1;
  let start = null;
  return {
    [SPAWN]: (h, fn, cap, arg) => {
      if (fn === 0n) return 0n;
      start ??= WebAssembly.promising(h.instance.exports._task);
      const id = next++;
      const t = { done: false, value: 0n };
      // The task starts from the microtask queue, which runs once the
      // spawning stack suspends: no instance is re-entered mid-call.
      t.promise = Promise.resolve()
        .then(() => start(fn, cap, arg))
        .catch(() => -1n)
        .then((v) => {
          t.done = true;
          t.value = v;
          return v;
        });
      tasks.set(id, t);
      return BigInt(id);
    },
    [JOIN]: (h, id) => {
      const t = tasks.get(Number(id));
      if (!t) return -22n;
      tasks.delete(Number(id));
      return t.done ? t.value : t.promise;
    },
  };
}
