package remote

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"github.com/horchestra/pack/pkg/logging"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// A target logs at pack's level, and its log comes back under pack's, the target named; both ends log the calls.
func TestTheTargetsLogIsPacks(t *testing.T) {
	fakeBin(t)
	var b lockedBuffer
	if err := logging.Setup(logging.Config{Level: "debug", Format: logging.FormatJSON}, &b); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logging.Setup(logging.Config{}, nil) })
	in := open(t, t.TempDir(), map[string]string{"target": "fake"})
	_ = in.Namespace()
	_ = in.(interface{ Close() error }).Close()
	s := b.String()
	for _, want := range []string{
		`"component":"target","target":"fake"`, // pack starts it
		`"target_component":"serve"`,           // the target's own line, logged again
		`"side":"server"`,                      // the target logs its calls
		`"side":"client"`,                      // pack logs its calls
		`"method":"/pack.target.v1.Target/Handshake"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("no %s in\n%s", want, s)
		}
	}
	if strings.Contains(s, "PACK_TARGET_TOKEN") || strings.Contains(s, `"token"`) {
		t.Errorf("a token in the log:\n%s", s)
	}
}
