package statusemitter

import "os"

// statExists is local to keep this package os-import-confined to one
// file. Mirrors subscriber/stat.go ; consolidating into a shared
// helper would force an `internal/internal` package, more pain than
// the duplication.
func statExists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}
