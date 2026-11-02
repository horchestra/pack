package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"maps"

	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/secretstore"
)

// storeSource is the environment's secret store as the render's CredentialSource: what is generated is kept at
// <prefix>/<name> (name: <installation>-<pointer with dashes>), so that the same command gives the same password
// on every run, and a reference reads the path it names.
type storeSource struct {
	ctx          context.Context
	store        secretstore.Store
	cfg          secretstore.Config
	installation string
	// readOnly: nothing is written — pack plan only looks. A password not kept yet has no value.
	readOnly bool
}

func (s *storeSource) Generate(pointer, key, leaf string) (render.Credential, error) {
	name := render.CredentialName(s.installation, pointer)
	path := s.cfg.PathOf(s.installation, name)
	fields, ok, err := s.store.Get(s.ctx, path)
	if err != nil {
		return render.Credential{}, err
	}
	c := render.Credential{Name: name, Leaf: leaf, Key: key, Store: path, Field: leaf}
	if ok && len(fields[leaf]) > 0 {
		c.Value = fields[leaf]
		return c, nil
	}
	if s.readOnly {
		return c, nil
	}
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return c, err
	}
	c.Value = base64.RawURLEncoding.EncodeToString(buf[:])
	next := map[string]string{}
	maps.Copy(next, fields)
	next[leaf] = c.Value
	if err := s.store.Put(s.ctx, path, next); err != nil {
		return c, err
	}
	return c, nil
}

func (s *storeSource) Reference(pointer, key, leaf, path, field string) (render.Credential, error) {
	fields, ok, err := s.store.Get(s.ctx, path)
	if err != nil {
		return render.Credential{}, err
	}
	if !ok {
		return render.Credential{}, fmt.Errorf("the store has no secret at %s", path)
	}
	value, ok := fields[field]
	if !ok {
		return render.Credential{}, fmt.Errorf("the secret at %s has no field %q (it has %v)", path, field, secretstore.Fields(fields))
	}
	// the place is in the name: a reference moved to another path is another secret to the plan, and the same one
	// again is not
	sum := sha256.Sum256([]byte(path + "#" + field))
	name := render.CredentialName(s.installation, pointer) + "-" + hex.EncodeToString(sum[:3])
	return render.Credential{Name: name, Leaf: leaf, Key: key, Value: value, Store: path, Field: field}, nil
}
