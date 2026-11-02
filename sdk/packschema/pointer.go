package packschema

import (
	"fmt"
	"reflect"
	"strings"
)

// Pointer is the JSON Pointer of a field of a pack, found from the field's address: the key of PackCompute and
// PackRules, which the author does not write by hand — a renamed json tag or a moved field moves the key with it.
//
//	func (o *Kafka) PackCompute() map[string]string {
//		return map[string]string{packschema.Pointer(o, &o.Kafka.Listeners): `…`}
//	}
//
// root is the receiver (or any pointer to the struct the schema is read off) and field is the address of a field
// inside it. A field not reached through a pointer that is set, or not of root at all, is a mistake in the pack:
// Pointer panics with the reason, the way regexp.MustCompile does, and the schema generation shows it.
func Pointer(root, field any) string {
	rv, fv := reflect.ValueOf(root), reflect.ValueOf(field)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || fv.Kind() != reflect.Pointer || fv.IsNil() {
		panic("packschema.Pointer: both arguments must be non-nil pointers")
	}
	if p, ok := find(rv.Elem(), fv.Pointer(), fv.Type().Elem(), ""); ok {
		return p
	}
	panic(fmt.Sprintf("packschema.Pointer: the %s is not a field of %s that the schema has (a field of a nil pointer, or json:\"-\")", fv.Type(), rv.Type()))
}

// find looks for the field at addr of type typ under v. Addresses alone do not tell a struct from its first
// field, so the type is compared too.
func find(v reflect.Value, addr uintptr, typ reflect.Type, path string) (string, bool) {
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return "", false
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return "", false
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() && !f.Anonymous { // encoding/json promotes the fields of an embedded unexported struct too
			continue
		}
		fv := v.Field(i)
		sub := path
		if !(f.Anonymous && len(jsonTagName(f)) == 0) { // an embedded struct's fields are the parent's
			name := jsonName(f)
			if len(name) == 0 {
				continue
			}
			sub = path + "/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(name)
		}
		if fv.Addr().Pointer() == addr && fv.Type() == typ && sub != path {
			return sub, true
		}
		if p, ok := find(fv, addr, typ, sub); ok {
			return p, true
		}
	}
	return "", false
}

// Dotted is the path of a field the way a formula or a rule reads it — "openbao.storage", as in
// `values.openbao.storage`, and the key of a held pack, as in `pack.keys.postgres` — found from the field's address
// like Pointer. The author writes no name of a field in an expression by hand.
//
//	storage := packschema.Dotted(o, &o.Bao.Storage)
//	rule := fmt.Sprintf("!has(values.%s) || size(values.%[1]s) == 0", storage)
//
// A name that cannot be a CEL identifier (it has a dot, a dash or a slash) is refused with a panic: the formula
// could not select it.
func Dotted(root, field any) string {
	segs := strings.Split(strings.TrimPrefix(Pointer(root, field), "/"), "/")
	for i, s := range segs {
		s = strings.NewReplacer("~1", "/", "~0", "~").Replace(s)
		if !celIdent(s) {
			panic(fmt.Sprintf("packschema.Dotted: %q is not a name a formula can select", s))
		}
		segs[i] = s
	}
	return strings.Join(segs, ".")
}

func celIdent(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i, r := range s {
		if r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}
