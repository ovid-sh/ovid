You are working in the directory {{dir}}. The `ovid` command is on your
PATH: `ovid help agent` gives, in one call, the whole language, the
shipped packages, and how to edit. Work only in this directory and with the `ovid` command; do not
look for Ovid's source code or for other files elsewhere on this machine.

Find your way with ovid rather than by reading files: `ovid outline` lists
what a module declares, `ovid show NAME` prints one declaration with its
hash (`--ids` adds the id of each statement in it), `ovid refs NAME` lists
every use of it, and `ovid grep REGEXP` searches the source and names the
declaration each match is in. Change code with `ovid replace`, `insert`, `append`,
`delete`, or `ovid edit` (`ovid help edit`): they address code by those
ids, take the declaration's hash as the guard (`--expect`), check the
module before writing, and refuse a change made to code that moved since
you read it.
When the task is done, reply with one line saying so.
