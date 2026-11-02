package compute

import (
	"reflect"
	"strings"
	"testing"
)

func TestReads(t *testing.T) {
	env, err := Env()
	if err != nil {
		t.Fatal(err)
	}
	a, err := Compile(env, `has(limits.memory) ? '-Xmx' + string(values.jdk.heapMB) + pack.keys.zk.instances.map(h, h + ':' + string(pack.keys.zk.ports.client)).join(',') : pack.values.realm`)
	if err != nil {
		t.Fatal(err)
	}
	got := Reads(a)
	want := []string{"limits.memory", "values.jdk.heapMB", "pack.keys.zk.instances", "pack.keys.zk.ports.client", "pack.values.realm"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reads: got %v want %v", got, want)
	}
}

func TestEvalAndFunctions(t *testing.T) {
	env, _ := Env()
	a, err := Compile(env, `indexMap(lists.range(me.replicas).map(i, me.service + '-' + string(i)), 1)`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Eval(env, a, map[string]any{"me": map[string]any{"replicas": 3, "service": "zk"}})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := out.(map[string]any)
	if !ok || len(m) != 3 || m["1"] != "zk-0" || m["3"] != "zk-2" {
		t.Fatalf("indexMap: %#v", out)
	}
	q, _ := Compile(env, `quantity("32Gi") / 1000 / 1048576 * 3 / 4`)
	v, err := Eval(env, q, nil)
	if err != nil || v.(int64) != 24576 {
		t.Fatalf("quantity: %v %v", v, err)
	}
	n, _ := Compile(env, `has(limits.x) ? 1 : null`)
	v, err = Eval(env, n, map[string]any{"limits": map[string]any{}})
	if err != nil || v != nil {
		t.Fatalf("null: %v %v", v, err)
	}
}

func TestQuantity(t *testing.T) {
	valid := map[string]int64{
		"0":      0,
		"0.5":    500,
		"500m":   500,
		"2":      2000,
		"1k":     1_000_000,
		"1M":     1_000_000_000,
		"1.5G":   1_500_000_000_000,
		"1T":     1_000_000_000_000_000,
		"1Ki":    1_024_000,
		"1Mi":    1_048_576_000,
		"1Gi":    1_073_741_824_000,
		"1Ti":    1_099_511_627_776_000,
		"1Pi":    1_125_899_906_842_624_000,
		"32Gi":   32 * (1 << 30) * 1000,
		" 500m ": 500, // spaces are not the quantity's
	}
	for in, want := range valid {
		got, err := Quantity(in)
		if err != nil || got != want {
			t.Errorf("%s: got %d want %d (%v)", in, got, want, err)
		}
	}
	invalid := map[string]string{
		"":      "empty",
		"x":     "not a number",
		"1.5.2": "not a number",
		"5X":    `unknown suffix "X"`,
		"1.5m":  "not a whole number of thousandths", // 1.5 thousandths
		"1e30":  "out of range",
	}
	for in, want := range invalid {
		if got, err := Quantity(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %d, %v — want an error with %q", in, got, err, want)
		}
	}
}

func TestResultType(t *testing.T) {
	env, err := Env()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		`string(pack.ordinal)`: "string",
		`pack.keys.zk.instances.map(h, h + ':' + string(pack.keys.zk.ports.client)).join(',')`: "string",
		`limits.memory / 2`:         "integer",
		`values.a + 1`:              "integer",
		`values.replicas * 2 > 3`:   "boolean",
		`[1, 2]`:                    "array",
		`{'a': 1}`:                  "object",
		`values.x`:                  "",
		`null`:                      "",
		`values.x > 1 ? 3 : null`:   "integer",
		`values.x > 1 ? null : 'a'`: "string",
		`has(limits.memory) ? string(limits.memory / 4) + 'MB' : null`: "string",
		`values.a ? (values.b ? 1 : null) : null`:                      "integer",
		`[values.a ? 1 : null]`:                                        "array",
	}
	for expr, want := range cases {
		got, err := ResultType(env, expr)
		if err != nil {
			t.Errorf("%s: %v", expr, err)
			continue
		}
		if got != want {
			t.Errorf("%s: got %q, want %q", expr, got, want)
		}
	}
	if _, err := ResultType(env, `1 + "a"`); err == nil {
		t.Error("a genuine type error must be reported")
	}
	// the rewrite works on its own parse: the formula still evaluates with the null branch
	a, _ := Compile(env, `values.x > 1 ? 3 : null`)
	if _, err := ResultType(env, `values.x > 1 ? 3 : null`); err != nil {
		t.Fatal(err)
	}
	out, err := Eval(env, a, map[string]any{"values": map[string]any{"x": 0}})
	if err != nil || out != nil {
		t.Errorf("null branch must survive: %v %v", out, err)
	}
}

// Reads: a literal index is part of the path, a dynamic one ends it with [];
// a path is reported once; what a comprehension binds is not a variable.
func TestReadsForms(t *testing.T) {
	env, err := Env()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		`values.arr[0]`:                                           {"values.arr.0"},
		`values.arr[0].x`:                                         {"values.arr.0.x"},
		`values[i]`:                                               {"values[]"},
		`values[me.index]`:                                        {"values[]", "me.index"},
		`values.a + values.a`:                                     {"values.a"},
		`values.list.map(v, v + values.offset)`:                   {"values.list", "values.offset"},
		`values.list.exists(v, v > values.min)`:                   {"values.list", "values.min"},
		`values.a.startsWith(values.b)`:                           {"values.a", "values.b"},
		`{'k': values.a, 'l': [values.b]}`:                        {"values.a", "values.b"},
		`values.a ? values.b : values.c`:                          {"values.a", "values.b", "values.c"},
		`string(me.replicas)`:                                     {"me.replicas"},
		`nothing.declared + 1`:                                    {}, // no variable of the four
		`pack.keys.zk.instances.map(h, h).join(',') + me.service`: {"pack.keys.zk.instances", "me.service"},
	}
	for expr, want := range cases {
		a, err := Compile(env, expr)
		if err != nil {
			t.Errorf("%s: %v", expr, err)
			continue
		}
		if got := Reads(a); !reflect.DeepEqual(got, want) && (len(got) > 0 || len(want) > 0) {
			t.Errorf("%s: reads %v, want %v", expr, got, want)
		}
	}
}

// A formula that does not parse is an error of Compile, with the expression's
// whereabouts in it.
func TestCompileRefuses(t *testing.T) {
	env, err := Env()
	if err != nil {
		t.Fatal(err)
	}
	for _, expr := range []string{"values.", "1 +", "a b c"} {
		if _, err := Compile(env, expr); err == nil {
			t.Errorf("%q must not compile", expr)
		}
	}
}

// Eval: the four variables exist even when the caller gave nothing; an
// evaluation failure (a division by zero) is the error, not a panic.
func TestEvalDefaultsAndErrors(t *testing.T) {
	env, err := Env()
	if err != nil {
		t.Fatal(err)
	}
	a, err := Compile(env, `has(values.x) || has(limits.x) || has(me.x) || has(pack.x)`)
	if err != nil {
		t.Fatal(err)
	}
	v, err := Eval(env, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v != false {
		t.Errorf("no variables given, nothing is there: %v", v)
	}
	div, err := Compile(env, `1 / 0`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Eval(env, div, nil); err == nil {
		t.Error("1 / 0 must fail at evaluation")
	}
}

// The two functions of the house refuse what is not theirs.
func TestFunctionArguments(t *testing.T) {
	env, err := Env()
	if err != nil {
		t.Fatal(err)
	}
	for expr, want := range map[string]string{
		`quantity('bogus')`:  "quantity",
		`quantity(1)`:        "no such overload", // a number is not the argument's type
		`indexMap('x', 1)`:   "no such overload",
		`indexMap([1], 'a')`: "no such overload",
	} {
		a, err := Compile(env, expr)
		if err != nil {
			t.Errorf("%s: %v", expr, err)
			continue
		}
		if _, err := Eval(env, a, nil); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want an error with %q, got %v", expr, want, err)
		}
	}
}

// Eval yields plain Go values: nil, bool, int64, float64, string, []byte,
// []any, map[string]any — a CEL value never leaks to a caller.
func TestEvalYieldsNativeValues(t *testing.T) {
	env, err := Env()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]any{
		`null`:                             nil,
		`1 == 1`:                           true,
		`42`:                               int64(42),
		`1u`:                               int64(1),
		`1.5`:                              1.5,
		`'s'`:                              "s",
		`b'ab'`:                            []byte("ab"),
		`[]`:                               []any{},
		`[1, 'a', null]`:                   []any{int64(1), "a", nil},
		`{}`:                               map[string]any{},
		`{'a': [1, {'b': 'x'}]}`:           map[string]any{"a": []any{int64(1), map[string]any{"b": "x"}}},
		`[{'k': 1}, {'k': 2}].map(m, m.k)`: []any{int64(1), int64(2)},
	}
	for expr, want := range cases {
		a, err := Compile(env, expr)
		if err != nil {
			t.Errorf("%s: %v", expr, err)
			continue
		}
		got, err := Eval(env, a, nil)
		if err != nil {
			t.Errorf("%s: %v", expr, err)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %#v, want %#v", expr, got, want)
		}
	}
}

// The environment knows no clock and no chance: now() is not a name it
// declares, so a formula that wants the time never type-checks.
func TestNoClockNoRandomness(t *testing.T) {
	env, err := Env()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ResultType(env, `now()`); err == nil {
		t.Error("now() must be undeclared: a formula is deterministic")
	}
}

// ResultType: the rest of the JSON types, and a formula that does not parse
// is the error.
func TestResultTypeMore(t *testing.T) {
	env, err := Env()
	if err != nil {
		t.Fatal(err)
	}
	for expr, want := range map[string]string{
		`true`:                 "boolean",
		`1.5`:                  "number",
		`1u`:                   "integer",
		`b'x'`:                 "", // bytes are no JSON type
		`dyn(values.x).a`:      "",
		`values.x == values.y`: "boolean",
	} {
		got, err := ResultType(env, expr)
		if err != nil {
			t.Errorf("%s: %v", expr, err)
			continue
		}
		if got != want {
			t.Errorf("%s: got %q, want %q", expr, got, want)
		}
	}
	if _, err := ResultType(env, `values.`); err == nil {
		t.Error("a formula that does not parse must be the error")
	}
}
