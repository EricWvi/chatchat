//go:build !unix

package cli

import "os"

// lockFile is a no-op where flock is unavailable; concurrent runs may then
// race on the history file.
func lockFile(*os.File) error { return nil }
