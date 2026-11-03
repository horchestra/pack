//go:build !unix

package remote

// alive cannot be told here: every owner is taken for running, and no container is removed as an orphan.
func alive(int) bool { return true }

// socketGroup cannot be told here: no group is added.
func socketGroup(string) (int, bool) { return 0, false }
