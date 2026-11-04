// Package packbin is the pack binary the operator runs: checked at start (pack version --json, К1), so that a
// missing or ancient pack is said at boot, not in the middle of a job.
package packbin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/horchestra/pack/pkg/api"
)

// Version runs pack version --json and says what it answered (A1).
func Version(ctx context.Context, pack string) (api.VersionResult, error) {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, pack, "version", "--json")
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return api.VersionResult{}, fmt.Errorf("the pack binary (%s): %w — jobs run it; set pack: in the configuration", pack, err)
	}
	var v api.VersionResult
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		return api.VersionResult{}, fmt.Errorf("the pack binary (%s): version --json: %w", pack, err)
	}
	if len(v.Version) == 0 {
		return api.VersionResult{}, fmt.Errorf("the pack binary (%s): version --json says no version", pack)
	}
	return v, nil
}
