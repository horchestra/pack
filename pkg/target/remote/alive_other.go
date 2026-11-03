//go:build !unix

package remote

// alive cannot be told here: every owner is taken for running, and no container is removed as an orphan.
func alive(int) bool { return true }
