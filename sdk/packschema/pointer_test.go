package packschema

import "testing"

type ptrInner struct {
	First int    `json:"first"`
	Tags  []int  `json:"tags,omitempty"`
	Skip  string `json:"-"`
}

type ptrEmbedded struct {
	Shared string `json:"shared"`
}

type ptrRoot struct {
	ptrEmbedded
	Inner ptrInner  `json:"in/ner"`
	Opt   *ptrInner `json:"opt,omitempty"`
	Held  *ptrInner `json:"held,omitempty"`
}

func TestPointer(t *testing.T) {
	r := &ptrRoot{Held: &ptrInner{}}
	for _, c := range []struct {
		field any
		want  string
	}{
		{&r.Inner, "/in~1ner"}, // a struct and its first field share an address: the type tells them apart
		{&r.Inner.First, "/in~1ner/first"},
		{&r.Inner.Tags, "/in~1ner/tags"},
		{&r.Shared, "/shared"},         // promoted from an embedded struct
		{&r.Opt, "/opt"},               // the pointer itself, nil
		{&r.Held.First, "/held/first"}, // through a set pointer
	} {
		if got := Pointer(r, c.field); got != c.want {
			t.Errorf("%T: %q, want %q", c.field, got, c.want)
		}
	}
}

func TestPointerRefusesWhatIsNotTheSchemas(t *testing.T) {
	r := &ptrRoot{}
	for name, field := range map[string]any{
		"json:\"-\"":           &r.Inner.Skip,
		"inside a nil pointer": &(&ptrInner{}).First,
		"not of root":          new(int),
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: must panic", name)
				}
			}()
			Pointer(r, field)
		}()
	}
}

type dottedRoot struct {
	Bao  ptrInner  `json:"openbao"`
	Held *ptrInner `json:"postgres,omitempty"`
	Odd  int       `json:"odd-name"`
}

func TestDotted(t *testing.T) {
	r := &dottedRoot{}
	if got := Dotted(r, &r.Bao.First); got != "openbao.first" {
		t.Errorf("%q", got)
	}
	if got := Dotted(r, &r.Held); got != "postgres" {
		t.Errorf("%q", got)
	}
	defer func() {
		if recover() == nil {
			t.Error("a name a formula cannot select must panic")
		}
	}()
	Dotted(r, &r.Odd)
}
