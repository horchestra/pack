package sdk_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/horchestra/pack/sdk"
	"github.com/horchestra/pack/sdk/packschema"
	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"

	"github.com/horchestra/pack/internal/testpacks/openbao"
	"github.com/horchestra/pack/internal/testpacks/tembo"
)

func generate(t *testing.T, roots ...sdk.Pack) string {
	t.Helper()
	dir := t.TempDir()
	if err := sdk.Generate(dir, roots...); err != nil {
		t.Fatal(err)
	}
	return dir
}

func read(t *testing.T, dir, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func tree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, rel)
		}
		return nil
	})
	slices.Sort(out)
	return out
}

// A pack that holds another writes both, though the one it holds is optional and absent.
func TestGenerateWritesThePackAndThePackItHolds(t *testing.T) {
	dir := generate(t, &openbao.OpenBao{})
	want := []string{
		"openbao/pack.yaml", "openbao/schema.json",
		"tembo/pack.yaml", "tembo/schema.json", "tembo/templates/tembo.env.ptmpl",
	}
	if got := tree(t, dir); !slices.Equal(got, want) {
		t.Fatalf("files:\n got %v\nwant %v", got, want)
	}
	// the content directory is read from disk, byte for byte
	if got := string(read(t, dir, "tembo/templates/tembo.env.ptmpl")); !strings.HasPrefix(got, "# the node's tags") {
		t.Fatalf("tembo.env.ptmpl = %q", got)
	}
}

// The settings of the pack it holds join the pack's schema: by reference, the pack's own schema stays its own.
func TestSchemaJoinsThePacksSettings(t *testing.T) {
	dir := generate(t, &openbao.OpenBao{})
	var s map[string]any
	if err := json.Unmarshal(read(t, dir, "openbao/schema.json"), &s); err != nil {
		t.Fatal(err)
	}
	props := s["properties"].(map[string]any)
	if ref := props["postgres"].(map[string]any)["$ref"]; ref != "pack://tembo#" {
		t.Fatalf("postgres = %v", props["postgres"])
	}
	bao := props["openbao"].(map[string]any)
	if f := s["x-pack"].(map[string]any)["content"].([]any)[0].(map[string]any); f["path"] != "/etc/openbao/config.json" || f["values"] != "openbao" {
		t.Fatalf("the config is a file of its values: %v", f)
	}
	if bao["properties"].(map[string]any)["log_level"].(map[string]any)["default"] != "info" {
		t.Fatal("SetDefaults is the default")
	}
	// required: a field without omitempty
	req := bao["required"].([]any)
	if len(req) != 1 || req[0] != "api_addr" {
		t.Fatalf("required = %v", req)
	}
	// the pack's x-pack is how it runs
	xp := s["x-pack"].(map[string]any)
	if xp["ports"].(map[string]any)["api"].(float64) != 8200 || xp["savepoints"].([]any)[0] != "postgres" {
		t.Fatalf("x-pack = %v", xp)
	}

	var ts map[string]any
	_ = json.Unmarshal(read(t, dir, "tembo/schema.json"), &ts)
	tp := ts["properties"].(map[string]any)
	if tp["pg_port"].(map[string]any)["x-pack-env"] != "TEMBO_PG_PORT" || tp["pg_port"].(map[string]any)["default"].(float64) != 5433 {
		t.Fatalf("pg_port = %v", tp["pg_port"])
	}
	if f := ts["x-pack"].(map[string]any)["content"].([]any)[0].(map[string]any); f["template"] != "tembo.env.ptmpl" || f["parseAs"] != "text" || f["mode"] != "0640" {
		t.Fatalf("node = %v", f)
	}
}

// pack.yaml carries everything Meta says, under the names the OCI image spec uses, and what the pack holds.
func TestPackFileCarriesMeta(t *testing.T) {
	dir := generate(t, &openbao.OpenBao{})
	var pf struct {
		Type        string            `json:"type"`
		Annotations map[string]string `json:"annotations"`
		Items       []struct {
			From string `json:"from"`
			Type string `json:"type"`
		} `json:"items"`
	}
	if err := yaml.Unmarshal(read(t, dir, "openbao/pack.yaml"), &pf); err != nil {
		t.Fatal(err)
	}
	a := pf.Annotations
	for k, v := range map[string]string{
		"pack.name": "openbao", "pack.version": "2.1.0",
		"org.opencontainers.image.title": "openbao", "org.opencontainers.image.version": "2.1.0",
		"org.opencontainers.image.description": "secrets", "org.opencontainers.image.licenses": "MPL-2.0",
		"example.org/tier": "core",
	} {
		if a[k] != v {
			t.Errorf("annotation %s = %q, want %q", k, a[k], v)
		}
	}
	if _, has := a["org.opencontainers.image.created"]; has {
		t.Error("created is written only when Meta says it: the artifact is the same byte for byte")
	}
	var froms []string
	for _, it := range pf.Items {
		froms = append(froms, it.From)
	}
	want := []string{"oci://images/openbao:2.1.0", "file://schema.json", "oci://packs/tembo:0.2.0"}
	if !slices.Equal(froms, want) {
		t.Fatalf("items = %v, want %v", froms, want)
	}
	// tembo has files: its pack file ships the templates directory
	var tp struct{ Items []struct{ From string } }
	_ = yaml.Unmarshal(read(t, dir, "tembo/pack.yaml"), &tp)
	if last := tp.Items[len(tp.Items)-1].From; last != "dir://templates/" {
		t.Fatalf("tembo items = %v", tp.Items)
	}
}

func TestGenerateIsStable(t *testing.T) {
	a, b := generate(t, &openbao.OpenBao{}), generate(t, &openbao.OpenBao{}, &tembo.Config{})
	for _, name := range tree(t, a) {
		if string(read(t, a, name)) != string(read(t, b, name)) {
			t.Errorf("%s differs between runs", name)
		}
	}
}

func TestPacks(t *testing.T) {
	ps, err := sdk.Packs(&openbao.OpenBao{})
	if err != nil || len(ps) != 2 || ps[0].Meta().Name != "openbao" || ps[1].Meta().Name != "tembo" {
		t.Fatalf("packs = %v, %v", ps, err)
	}
}

func TestVerify(t *testing.T) {
	for _, p := range []sdk.Pack{&openbao.OpenBao{}, &tembo.Config{}} {
		ps, err := sdk.Verify(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, pr := range ps {
			t.Errorf("%s: %s", p.Meta().Name, pr)
		}
	}
}

type probe struct {
	Name string `json:"name,omitempty"`
	meta sdk.Meta
}

func (*probe) SetDefaults()     {}
func (p *probe) Meta() sdk.Meta { return p.meta }
func (*probe) PackRoot() packschema.Root {
	return packschema.Root{Content: []packschema.File{{Path: "/etc/p.conf", Template: "p.ptmpl", ParseAs: "text"}}}
}

func TestContentIsAnOrdinaryDirectory(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(dir, "content", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("p.ptmpl", "hello")
	write("sub/dir/extra.sh", "x")
	outside := filepath.Join(t.TempDir(), "secret")
	_ = os.WriteFile(outside, []byte("do not read"), 0o600)
	if err := os.Symlink(outside, filepath.Join(dir, "content", "link")); err != nil {
		t.Skip("no symlinks here")
	}
	p := &probe{meta: sdk.Meta{Name: "probe", Version: "1", Dir: dir}}
	out := generate(t, p)
	want := []string{"probe/pack.yaml", "probe/schema.json", "probe/templates/p.ptmpl", "probe/templates/sub/dir/extra.sh"}
	if got := tree(t, out); !slices.Equal(got, want) {
		t.Fatalf("files:\n got %v\nwant %v", got, want)
	}
}

// Image is a repository of the registry the pack is pushed to: the pack file says oci://, the author does not.
func TestImageHasNoScheme(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "content"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "content", "p.ptmpl"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	pf, err := sdk.PackFile(&probe{meta: sdk.Meta{Name: "probe", Version: "1", Dir: dir, Image: "images/app:1"}})
	if err != nil {
		t.Fatal(err)
	}
	if pf.Items[0].From != "oci://images/app:1" {
		t.Errorf("the image in the pack file = %s", pf.Items[0].From)
	}
	if _, err := sdk.PackFile(&probe{meta: sdk.Meta{Name: "probe", Version: "1", Dir: dir, Image: "oci://images/app:1"}}); err == nil || !strings.Contains(err.Error(), "no scheme") {
		t.Errorf("a scheme in Image: %v", err)
	}
}

// The CLI's generate writes the pack alone, and its file says where it and the pack it holds are published.
func TestGenerateInWritesThePackAlone(t *testing.T) {
	dir := t.TempDir()
	if err := sdk.GenerateIn(dir, "team", &openbao.OpenBao{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tembo")); err == nil {
		t.Error("the pack it holds is generated on its own")
	}
	pf := string(read(t, dir, "openbao/pack.yaml"))
	if !strings.Contains(pf, "repository: team/openbao:") || !strings.Contains(pf, "oci://team/tembo:") {
		t.Errorf("pack.yaml:\n%s", pf)
	}
}

func TestMistakesAreRefused(t *testing.T) {
	dir := t.TempDir() // no content/ at all
	for name, p := range map[string]*probe{
		"a template the content does not have": {meta: sdk.Meta{Name: "probe", Version: "1", Dir: dir}},
		"no version":                           {meta: sdk.Meta{Name: "probe", Dir: dir}},
		"an annotation the sdk writes itself":  {meta: sdk.Meta{Name: "probe", Version: "1", Dir: dir, Annotations: map[string]string{"pack.name": "x"}}},
		"no name":                              {meta: sdk.Meta{Version: "1", Dir: dir}},
	} {
		if err := sdk.Generate(t.TempDir(), p); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

// The pack's directory is where its Meta is defined, unless Meta.Dir says otherwise.
func TestDirIsWhereMetaIsDefined(t *testing.T) {
	out := generate(t, &tembo.Config{})
	if got := tree(t, out); !slices.Contains(got, "tembo/templates/tembo.env.ptmpl") {
		t.Fatalf("files = %v", got)
	}
}

// The generator is a cobra command an author adds his own subcommands to.
func TestCommand(t *testing.T) {
	dir := t.TempDir()
	cmd := sdk.Command(&openbao.OpenBao{})
	mine := false
	cmd.AddCommand(&cobra.Command{Use: "values", Run: func(*cobra.Command, []string) { mine = true }})

	cmd.SetArgs([]string{"generate", "-o", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := tree(t, dir); !slices.Contains(got, "openbao/schema.json") || slices.Contains(got, "tembo/pack.yaml") {
		t.Fatalf("files = %v", got)
	}

	cmd.SetArgs([]string{"values"})
	if err := cmd.Execute(); err != nil || !mine {
		t.Fatalf("the author's own subcommand: %v %v", err, mine)
	}

	cmd.SetArgs([]string{"schema", "nope", "extra"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("a wrong number of arguments is cobra's to refuse")
	}
	cmd.SetArgs([]string{"nonsense"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("an unknown command is refused")
	}
}

// Twin holds two packs of one kind, both there: a stand has two databases.
type Twin struct {
	A tembo.Config  `json:"a"`
	B *tembo.Config `json:"b,omitempty"`
}

func (t *Twin) SetDefaults() { t.B = &tembo.Config{ID: "b"} }
func (*Twin) Meta() sdk.Meta {
	return sdk.Meta{Name: "twin", Version: "1", Dir: "/nonexistent"}
}
func (*Twin) PackRoot() packschema.Root { return packschema.Root{} }

func TestTwoPacksOfOneKind(t *testing.T) {
	if err := sdk.Generate(t.TempDir(), &Twin{}); err != nil {
		t.Fatal(err)
	}
}

// SelfHolder holds itself.
type SelfHolder struct {
	Again *SelfHolder `json:"again,omitempty"`
}

func (*SelfHolder) SetDefaults()              {}
func (*SelfHolder) Meta() sdk.Meta            { return sdk.Meta{Name: "self", Version: "1", Dir: "/nonexistent"} }
func (*SelfHolder) PackRoot() packschema.Root { return packschema.Root{} }

func TestAPackThatHoldsItselfIsRefused(t *testing.T) {
	if _, err := sdk.Packs(&SelfHolder{}); err == nil || !strings.Contains(err.Error(), "holds itself") {
		t.Fatalf("err = %v", err)
	}
}
