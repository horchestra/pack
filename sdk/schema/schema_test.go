package schema

import (
	"encoding/json"
	"testing"
)

const kafka = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object", "additionalProperties": false,
  "x-pack": {"readiness": {"tcp": "broker"}, "ports": {"broker": 9092, "internal": 9093},
    "content": [{"path": "/opt/kafka/config/server.properties", "values": "conf"}, {"path": "/opt/kafka/config/kafka_jaas.conf", "template": "jaas.tmpl", "values": "jaas", "parseAs": "properties", "mode": "0400"}]},
  "properties": {
    "jdk": {"$ref": "pack://jdk#", "x-pack-mount": "/opt/java"},
    "conf": {
      "type": "object", "additionalProperties": false,
      "properties": {
        "log.dirs": {"type": "string", "default": "/var/lib/kafka/data"},
        "min.insync.replicas": {"type": "integer", "minimum": 1, "default": 1, "x-pack-arg": {"flag": "--override", "prefix": "min.insync.replicas="}},
        "zookeeper.connect": {"type": "string", "x-pack-compute": "pack.keys['zookeeper'].instances.map(h, h + ':2181').join(',')"}
      }
    },
    "jaas": {
      "type": "object", "required": ["password"],
      "properties": {
        "mechanism": {"type": "string", "enum": ["SCRAM-SHA-512", "PLAIN"], "default": "SCRAM-SHA-512"},
        "password": {"type": "string", "format": "password"}
      }
    },
    "heapOpts": {"type": "string", "x-pack-env": "KAFKA_HEAP_OPTS", "x-pack-compute": "'-Xmx' + string(values.jdk.heapMB) + 'm'"}
  }
}`

const jdk = `{"type": "object", "additionalProperties": false,
  "properties": {"gc": {"type": "string", "enum": ["G1", "ZGC"], "default": "G1"},
                 "heapMB": {"type": "integer", "minimum": 128, "default": 1024, "x-pack-compute": "has(limits.memory) ? limits.memory / 1048576 * 3 / 4 : null"}}}`

func parseKafka(t *testing.T) *Schema {
	t.Helper()
	s, err := Parse([]byte(kafka))
	if err != nil {
		t.Fatal(err)
	}
	lib, err := Parse([]byte(jdk))
	if err != nil {
		t.Fatal(err)
	}
	s.Foreign["pack://jdk"] = lib
	return s
}

func TestParse_AnnotationsAndOrder(t *testing.T) {
	s := parseKafka(t)
	if got := s.Root.Order; len(got) != 4 || got[0] != "jdk" || got[1] != "conf" || got[3] != "heapOpts" {
		t.Fatalf("document order lost: %v", got)
	}
	conf := s.Root.Properties["conf"]
	if conf.File == nil || conf.File.Path != "/opt/kafka/config/server.properties" || conf.File.Format != "properties" {
		t.Fatalf("the file of its values is not on the node: %+v", conf.File)
	}
	if a := conf.Properties["min.insync.replicas"].Arg; a == nil || a.Flag != "--override" || a.Prefix != "min.insync.replicas=" {
		t.Fatalf("x-pack-arg not read: %+v", a)
	}
	if s.Root.Properties["heapOpts"].Env != "KAFKA_HEAP_OPTS" || len(s.Root.Properties["heapOpts"].Compute) == 0 {
		t.Fatal("x-pack-env / x-pack-compute not read")
	}
	if s.Root.Properties["jdk"].Mount != "/opt/java" || !s.Root.Properties["jdk"].IsForeign() {
		t.Fatal("foreign ref not read")
	}
	root, err := s.Root.RootAnnotation()
	if err != nil || root.Readiness.TCP != "broker" || root.Ports["internal"] != 9093 {
		t.Fatalf("x-pack root: %+v %v", root, err)
	}
	if !s.Root.Properties["jaas"].Properties["password"].IsPassword() {
		t.Fatal("password leaf")
	}
}

func TestParse_UnknownAnnotationKeyIsRefused(t *testing.T) {
	_, err := Parse([]byte(`{"type":"object","properties":{"c":{"x-pack-arg":{"flag":"--a","fromat":"x"}}}}`))
	if err == nil {
		t.Fatal("expected refusal of unknown key")
	}
}

func TestDefaults(t *testing.T) {
	s := parseKafka(t)
	d, ok, err := s.Defaults()
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	b, _ := json.Marshal(d)
	want := `{"conf":{"log.dirs":"/var/lib/kafka/data","min.insync.replicas":1},"jaas":{"mechanism":"SCRAM-SHA-512"},"jdk":{"gc":"G1","heapMB":1024}}`
	if string(b) != want {
		t.Fatalf("defaults:\n got %s\nwant %s", b, want)
	}
}

func TestVisit_FollowsForeignRefsWithInstancePath(t *testing.T) {
	s := parseKafka(t)
	var computes []string
	err := s.Visit(func(ptr string, _ *Schema, n *Node) error {
		if len(n.Compute) > 0 {
			computes = append(computes, ptr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/conf/zookeeper.connect", "/heapOpts", "/jdk/heapMB"}
	if len(computes) != 3 {
		t.Fatalf("computes: %v", computes)
	}
	found := map[string]bool{}
	for _, c := range computes {
		found[c] = true
	}
	for _, w := range want {
		if !found[w] {
			t.Fatalf("missing %s in %v", w, computes)
		}
	}
	_, n, err := s.Lookup("/jdk/heapMB")
	if err != nil || len(n.Compute) == 0 {
		t.Fatalf("lookup through foreign ref: %v %+v", err, n)
	}
}

func TestCompile_PasswordTakesOnlyARef(t *testing.T) {
	s := parseKafka(t)
	c, err := s.Compile("pack://kafka")
	if err != nil {
		t.Fatal(err)
	}
	ok := map[string]any{
		"jdk":  map[string]any{"gc": "G1"},
		"jaas": map[string]any{"password": map[string]any{"secretRef": map[string]any{"name": "s", "key": "k"}}}}
	if v := c.Validate(ok); len(v) != 0 {
		t.Fatalf("valid doc refused: %v", v)
	}
	bad := map[string]any{"jaas": map[string]any{"password": "plaintext"}, "jdk": map[string]any{"gc": "CMS"}, "unknown": 1}
	v := c.Validate(bad)
	if len(v) < 3 {
		t.Fatalf("expected refusals for plaintext password, bad enum and an unknown property, got %v", v)
	}
	t.Log(v)
}

// x-pack-rules parses strictly: a well-formed rule lands on the node, an
// unknown key, an empty expression or a non-list are refusals.
func TestParseRules(t *testing.T) {
	s, err := Parse([]byte(`{"type":"object","properties":{
		"storage": {"type":"array","x-pack-rules":[
			{"rule":"!has(values.dep) || size(values.storage) == 0","message":"storage is derived","phases":["preMutate","postMutate"]},
			{"rule":"size(values.storage) > 0"}
		]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	rules := s.Root.Properties["storage"].Rules
	if len(rules) != 2 {
		t.Fatalf("rules = %v", rules)
	}
	if rules[0].Message != "storage is derived" || len(rules[0].Phases) != 2 || rules[0].Phases[0] != "preMutate" {
		t.Fatalf("rule[0] = %+v", rules[0])
	}
	if len(rules[1].Phases) != 0 {
		t.Fatalf("no phases means the default: %+v", rules[1])
	}

	for name, raw := range map[string]string{
		"unknown key":   `{"type":"object","x-pack-rules":[{"rule":"true","bogus":1}]}`,
		"empty rule":    `{"type":"object","x-pack-rules":[{"message":"no expression"}]}`,
		"not a list":    `{"type":"object","x-pack-rules":{"rule":"true"}}`,
		"not an object": `{"type":"object","x-pack-rules":["true"]}`,
	} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}
