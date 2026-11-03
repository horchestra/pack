package session

import (
	"os"
	"strings"
	"testing"

	"github.com/horchestra/pack/internal/release"
	"github.com/horchestra/pack/pkg/target/remote"
)

func TestPinRuntime(t *testing.T) {
	lock := &release.Lock{Targets: map[string]release.TargetLock{"ansible": {Image: "oci://reg.example/targets/ansible:1", Digest: "sha256:" + strings.Repeat("a", 64)}}}
	set := map[string]string{"runtime_type": "docker"}
	if err := pinRuntime("ansible", set, lock, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if got := set[remote.SettingImage]; got != "reg.example/targets/ansible@sha256:"+strings.Repeat("a", 64) {
		t.Errorf("image = %q", got)
	}
	if home := set[remote.SettingHome]; !strings.HasSuffix(home, "/home/ansible") {
		t.Errorf("home = %q", home)
	} else if st, err := os.Stat(home); err != nil || !st.IsDir() {
		t.Errorf("home not made: %v", err)
	}
	if err := pinRuntime("ansible", map[string]string{"runtime_type": "docker", "runtime_home": "/x"}, lock, t.TempDir()); err == nil {
		t.Error("a home the file says")
	}
	if err := pinRuntime("docker", map[string]string{"runtime_type": "docker"}, lock, t.TempDir()); err == nil || !strings.Contains(err.Error(), "pack refresh") {
		t.Errorf("not locked: %v", err)
	}
	if err := pinRuntime("ansible", map[string]string{"runtime_image": "x"}, lock, t.TempDir()); err == nil {
		t.Error("an image the file says")
	}
	native := map[string]string{}
	if err := pinRuntime("ansible", native, nil, t.TempDir()); err != nil || len(native) > 0 {
		t.Errorf("native: %v %v", native, err)
	}
}

func TestPinRuntimeOfATargetInPack(t *testing.T) {
	if err := pinRuntime("memory", map[string]string{"runtime_type": "docker"}, nil, ""); err == nil || !strings.Contains(err.Error(), "in pack itself") {
		t.Errorf("memory in a container: %v", err)
	}
	if err := pinRuntime("memory", map[string]string{"runtime_network": "host"}, nil, ""); err == nil {
		t.Error("a runtime setting of memory")
	}
	if err := pinRuntime("memory", map[string]string{"observe": "true"}, nil, ""); err != nil {
		t.Errorf("memory's own settings: %v", err)
	}
}
