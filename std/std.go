// Package std embeds the Ovid packages the toolchain ships (ovid/io, ovid/mem).
// A module import that is not a directory of the module resolves here.
package std

import "embed"

//go:embed ovid
var FS embed.FS
