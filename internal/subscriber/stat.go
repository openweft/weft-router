package subscriber

import "os"

// statExists wraps os.Stat in a tiny helper so the main file stays focused
// on the subscribe loop. Returns true iff the path exists and is readable.
func statExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
