package release

import (
	"os"
	"os/exec"
	"strings"
)

// Revision is the revision of the release's inputs: PACK_REVISION where a runner (an operator) says it, else the
// commit the release's directory is at, when it is in git; "" — not known. A plan and the record of its saga say
// it, so what was applied names what it was applied from.
func Revision(dir string) string {
	if v := os.Getenv("PACK_REVISION"); len(v) > 0 {
		return v
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
