package authz

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/horchestra/pack/operator/db"
)

func newOnSQLite(t *testing.T, auth, devUser string) *Authz {
	t.Helper()
	d, err := db.Open(context.Background(), "sqlite://"+filepath.Join(t.TempDir(), "operator.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	a, err := New(d, auth, devUser)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SeedProject("dev"); err != nil {
		t.Fatal(err)
	}
	return a
}

// Every role of К7 holds what it should in its domain — and no more.
func TestRoles(t *testing.T) {
	a := newOnSQLite(t, "header", "")
	type want struct{ obj, act string }
	// role → what it may, and one thing it may not
	cases := map[string]struct {
		may    []want
		mayNot want
	}{
		"viewer":   {[]want{{"project", "read"}, {"release:/releases/prod", "read"}}, want{"release:/releases/prod", "edit"}},
		"operator": {[]want{{"release:/releases/prod", "edit"}, {"release:/releases/prod", "plan"}, {"release:/releases/prod", "request"}}, want{"release:/releases/prod", "approve"}},
		"approver": {[]want{{"release:/releases/prod", "approve"}}, want{"change:/c1", "merge"}},
		"merger":   {[]want{{"change:/c1", "merge"}}, want{"project", "admin"}},
		"author":   {[]want{{"pack:/packs/kv", "edit"}, {"pack:/packs/kv", "author"}}, want{"release:/releases/prod", "edit"}},
		"admin":    {[]want{{"project", "admin"}, {"secret:/git/dev", "admin"}, {"release:/releases/prod", "approve"}, {"change:/c1", "merge"}}, want{"", ""}},
	}
	for role, c := range cases {
		user := "user-of-" + role
		if err := a.Grant(user, role, "dev"); err != nil {
			t.Fatal(err)
		}
		for _, m := range c.may {
			ok, err := a.Enforce(user, "dev", m.obj, m.act)
			if err != nil || !ok {
				t.Errorf("%s may %s %s: %v, %v", role, m.act, m.obj, ok, err)
			}
		}
		if len(c.mayNot.obj) > 0 {
			if ok, err := a.Enforce(user, "dev", c.mayNot.obj, c.mayNot.act); err != nil || ok {
				t.Errorf("%s may not %s %s: %v, %v", role, c.mayNot.act, c.mayNot.obj, ok, err)
			}
		}
		// a domain is a project: the role of another project is not this one's
		if ok, err := a.Enforce(user, "other", "release:/releases/prod", "read"); err != nil || ok {
			t.Errorf("%s in another domain: %v, %v", role, ok, err)
		}
	}
	if err := a.Grant("x", "superuser", "dev"); err == nil {
		t.Error("a role that is no role")
	}
}

// auth: none — the dev user may everything, everywhere; any other name, nothing.
func TestAuthNone(t *testing.T) {
	a := newOnSQLite(t, "none", "dev")
	for _, act := range []string{"read", "edit", "approve", "merge", "admin"} {
		if ok, err := a.Enforce("dev", "anywhere", "release:/x", act); err != nil || !ok {
			t.Errorf("dev may %s: %v, %v", act, ok, err)
		}
	}
	if ok, err := a.Enforce("mallory", "anywhere", "release:/x", "read"); err != nil || ok {
		t.Errorf("another name may nothing: %v, %v", ok, err)
	}
}

// The policy survives the process: a new enforcer on the same database holds what the old one granted.
func TestPolicyPersists(t *testing.T) {
	dir := t.TempDir()
	dsn := "sqlite://" + filepath.Join(dir, "operator.db")
	d, err := db.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(d, "header", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SeedProject("dev"); err != nil {
		t.Fatal(err)
	}
	if err := a.Grant("alice", "approver", "dev"); err != nil {
		t.Fatal(err)
	}
	d.Close()

	d, err = db.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	a, err = New(d, "header", "")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := a.Enforce("alice", "dev", "release:/releases/prod", "approve"); err != nil || !ok {
		t.Errorf("alice is an approver still: %v, %v", ok, err)
	}
	users, err := a.Users("dev")
	if err != nil || len(users) != 1 {
		t.Errorf("the users of dev: %v, %v", users, err)
	}
}
