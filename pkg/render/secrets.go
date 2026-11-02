package render

import (
	"fmt"
	"strings"

	"github.com/horchestra/pack/sdk/schema"
)

// CredentialSource is where the passwords come from that the operator did not write: the secret store of the
// environment. With one in the Context, a password leaf of a selected key that the values leave out is generated
// — once, kept in the store, the same on every run — and a leaf given as
//
//	password:
//	  openbao:
//	    path: team/db
//	    key: password
//
// is read from that path of the store. Either way the document gets the usual secretRef and the Release carries
// the Credential.
type CredentialSource interface {
	// Generate is the credential of the password leaf at pointer (leaf is its last segment, key the system key):
	// the one kept for it, or a new one it keeps.
	Generate(pointer, key, leaf string) (Credential, error)
	// Reference is the credential of the leaf at pointer, read from field of the secret at path.
	Reference(pointer, key, leaf, path, field string) (Credential, error)
}

// provide fills the password leaves of the selected keys that the values do not.
func (r *render) provide() error {
	if r.ctx.Secrets == nil {
		return nil
	}
	for _, key := range r.keys {
		comp := r.b.Packs[r.b.KeyPack[key]]
		if comp == nil || comp.Schema == nil {
			continue
		}
		if err := r.provideIn(comp.Schema.Root, "/"+key, key); err != nil {
			return err
		}
	}
	return nil
}

func (r *render) provideIn(n *schema.Node, ptr, key string) error {
	required := map[string]bool{}
	for _, name := range n.Required {
		required[name] = true
	}
	for _, name := range n.Order {
		child := n.Properties[name]
		if child == nil {
			continue
		}
		at := ptr + "/" + name
		if len(child.Properties) > 0 {
			if cur, _ := get(r.doc, at); cur != nil {
				if err := r.provideIn(child, at, key); err != nil {
					return err
				}
			}
			continue
		}
		if !child.IsPassword() {
			continue
		}
		cur, has := get(r.doc, at)
		switch {
		case has && cur != nil:
			given, _ := cur.(map[string]any)
			ref, ok := given["openbao"].(map[string]any)
			if !ok {
				continue // a secretRef of the operator's
			}
			path, _ := ref["path"].(string)
			field, _ := ref["key"].(string)
			if len(path) == 0 || len(field) == 0 {
				return fmt.Errorf("%s: openbao: path and key say where the secret is", at)
			}
			c, err := r.ctx.Secrets.Reference(at, key, name, path, field)
			if err != nil {
				return fmt.Errorf("%s: %w", at, err)
			}
			if err := r.putCredential(at, c); err != nil {
				return err
			}
		case required[name] && !child.HasDefault:
			c, err := r.ctx.Secrets.Generate(at, key, name)
			if err != nil {
				return fmt.Errorf("%s: %w", at, err)
			}
			if err := r.putCredential(at, c); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *render) putCredential(ptr string, c Credential) error {
	c.Pointer = r.b.External(ptr) // the operator's pointer: WriteBack puts the reference in his values
	r.credentials = append(r.credentials, c)
	return set(r.doc, ptr, map[string]any{"secretRef": map[string]any{"name": c.Name, "key": c.Leaf}})
}

// CredentialName is the name of the Secret a credential at pointer is kept under: <installation>-<pointer with
// dashes>, as Provision names it.
func CredentialName(installation, pointer string) string {
	return installation + "-" + strings.Join(schema.Split(pointer), "-")
}

// WriteBack puts into the operator's values the references the render wrote for the credentials it took, so that
// the installation's record holds a document that renders again without a source, as it does for the credentials
// Provision made.
func WriteBack(v *Values, credentials []Credential) error {
	if v.Values == nil {
		v.Values = map[string]any{}
	}
	for _, c := range credentials {
		if len(c.Pointer) == 0 {
			continue
		}
		ptr := strings.TrimPrefix(c.Pointer, "/")
		if err := set(v.Values, ptr, map[string]any{"secretRef": map[string]any{"name": c.Name, "key": c.Leaf}}); err != nil {
			return err
		}
	}
	return nil
}
