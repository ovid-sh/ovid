// A std line changes where imports resolve, but grants no syscall. .std is
// a dot directory, so the module walk leaves it out.
module app
entry app
std .std
