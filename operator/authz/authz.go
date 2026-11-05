// Package authz is the operator's permissions (К7): casbin RBAC with domains — a domain is a project, the
// objects its releases, packs, change requests and secrets. The policy lives in the operator's database; the
// roles (viewer, operator, approver, merger, author, admin) are seeded for a project when it first appears.
//
// With auth: none (only where nothing else is possible, and said so in the log at start) the dev user is
// admin in every domain; the users of auth: local and auth: oidc hold only what the policy grants (authn
// says who they are, stage 3 — here is what they may).
package authz

import (
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"github.com/casbin/casbin/v3"
	"github.com/casbin/casbin/v3/model"
	"github.com/casbin/casbin/v3/persist"

	"github.com/horchestra/pack-operator/db"
)

// Model is the casbin model of К7, fixed: RBAC with domains, the objects matched as paths.
const Model = `
[request_definition]
r = sub, dom, obj, act
[policy_definition]
p = sub, dom, obj, act
[role_definition]
g = _, _, _
[policy_effect]
e = some(where (p.eft == allow))
[matchers]
m = g(r.sub, p.sub, r.dom) && r.dom == p.dom && keyMatch2(r.obj, p.obj) && r.act == p.act
`

// The roles, with what each may in its domain. A wider role holds the narrower one's rights: they are spelled
// out, one row each, rather than inherited — what the policy says is what a person reads.
var roles = map[string][][2]string{ // role → [object, action]
	"viewer": {
		{"project", "read"}, {"release:/*", "read"}, {"pack:/*", "read"}, {"change:/*", "read"}, {"secret:/*", "read"},
	},
	"operator": {
		{"project", "read"}, {"release:/*", "read"}, {"pack:/*", "read"}, {"change:/*", "read"}, {"secret:/*", "read"},
		{"release:/*", "edit"}, {"release:/*", "plan"}, {"release:/*", "request"},
	},
	"approver": {
		{"project", "read"}, {"release:/*", "read"}, {"pack:/*", "read"}, {"change:/*", "read"}, {"secret:/*", "read"},
		{"release:/*", "edit"}, {"release:/*", "plan"}, {"release:/*", "request"},
		{"release:/*", "approve"},
	},
	"merger": {
		{"project", "read"}, {"release:/*", "read"}, {"pack:/*", "read"}, {"change:/*", "read"}, {"secret:/*", "read"},
		{"release:/*", "edit"}, {"release:/*", "plan"}, {"release:/*", "request"},
		{"release:/*", "approve"},
		{"change:/*", "merge"},
	},
	"author": {
		{"project", "read"}, {"release:/*", "read"}, {"pack:/*", "read"}, {"change:/*", "read"},
		{"pack:/*", "edit"}, {"pack:/*", "author"},
	},
	"admin": {
		{"project", "admin"}, {"release:/*", "read"}, {"release:/*", "edit"}, {"release:/*", "plan"},
		{"release:/*", "request"}, {"release:/*", "approve"}, {"pack:/*", "read"}, {"pack:/*", "edit"},
		{"pack:/*", "author"}, {"change:/*", "read"}, {"change:/*", "merge"}, {"secret:/*", "read"},
		{"secret:/*", "admin"}, {"project", "read"},
	},
}

// RoleNames are the roles, for whoever lists them.
func RoleNames() []string {
	out := make([]string, 0, len(roles))
	for r := range roles {
		out = append(out, r)
	}
	slices.Sort(out)
	return out
}

// Authz answers may(user, domain, object, action). With auth none the dev user may everything.
type Authz struct {
	e       *casbin.Enforcer
	auth    string // none
	devUser string
}

// New builds the enforcer on the policy the database holds (the adapter keeps it there).
func New(d *db.DB, auth, devUser string) (*Authz, error) {
	m, err := model.NewModelFromString(Model)
	if err != nil {
		return nil, fmt.Errorf("the casbin model: %w", err)
	}
	e, err := casbin.NewEnforcer(m, &Adapter{DB: d.DB, pg: !d.SQLite()})
	if err != nil {
		return nil, fmt.Errorf("the enforcer: %w", err)
	}
	return &Authz{e: e, auth: auth, devUser: devUser}, nil
}

// Enforce says whether user may act on obj in dom. Object and action are К7's: release:<path>, pack:<path>,
// change:<id>, project, secret:<name>; read, edit, plan, request, approve, merge, author, admin.
func (a *Authz) Enforce(user, dom, obj, act string) (bool, error) {
	if a.auth == "none" && user == a.devUser {
		return true, nil
	}
	return a.e.Enforce(user, dom, obj, act)
}

// Grant gives user a role in a domain; Revoke takes it back.
func (a *Authz) Grant(user, role, dom string) error {
	if _, ok := roles[role]; !ok {
		return fmt.Errorf("no role %s: the roles are %s", role, strings.Join(RoleNames(), ", "))
	}
	_, err := a.e.AddGroupingPolicy(user, role, dom)
	return err
}

// Revoke takes user's role in dom back.
func (a *Authz) Revoke(user, role, dom string) error {
	_, err := a.e.RemoveGroupingPolicy(user, role, dom)
	return err
}

// Users is the (user, role) pairs of a domain.
func (a *Authz) Users(dom string) ([][2]string, error) {
	gs, err := a.e.GetFilteredGroupingPolicy(2, dom)
	if err != nil {
		return nil, err
	}
	out := make([][2]string, 0, len(gs))
	for _, g := range gs {
		out = append(out, [2]string{g[0], g[1]})
	}
	return out, nil
}

// SeedProject makes the roles of К7 hold in the project's domain when nothing holds there yet, and — with auth
// none — makes the dev user its admin. It never rewrites what a person granted.
func (a *Authz) SeedProject(dom string) error {
	for role, perms := range roles {
		for _, oa := range perms {
			policy := []string{role, dom, oa[0], oa[1]}
			has, err := a.e.HasPolicy(policy)
			if err != nil {
				return err
			}
			if !has {
				if _, err := a.e.AddPolicy(policy); err != nil {
					return err
				}
			}
		}
	}
	if a.auth == "none" {
		has, err := a.e.HasGroupingPolicy(a.devUser, "admin", dom)
		if err != nil {
			return err
		}
		if !has {
			if _, err := a.e.AddGroupingPolicy(a.devUser, "admin", dom); err != nil {
				return err
			}
		}
	}
	return nil
}

// Adapter keeps the casbin policy in the operator's database (casbin_rule, migration 0001): PostgreSQL and
// SQLite alike, through database/sql; pg says the placeholders are $N, not ?.
type Adapter struct {
	DB *sql.DB
	pg bool
}

// bind numbers the ? of a query for PostgreSQL ($1, $2 …); SQLite takes them as they are.
func (a *Adapter) bind(query string) string {
	if !a.pg {
		return query
	}
	var b strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

var _ persist.Adapter = (*Adapter)(nil)

// LoadPolicy reads every rule into the model.
func (a *Adapter) LoadPolicy(m model.Model) error {
	rows, err := a.DB.Query(a.bind(`select ptype, v0, v1, v2, v3, v4, v5 from casbin_rule`))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ptype string
		var v [6]string
		if err := rows.Scan(&ptype, &v[0], &v[1], &v[2], &v[3], &v[4], &v[5]); err != nil {
			return err
		}
		rule := make([]string, 0, 6)
		for _, s := range v {
			if len(s) == 0 {
				break
			}
			rule = append(rule, s)
		}
		persist.LoadPolicyLine(ptype+`, `+strings.Join(rule, `, `), m)
	}
	return rows.Err()
}

// SavePolicy rewrites the policy whole.
func (a *Adapter) SavePolicy(m model.Model) error {
	tx, err := a.DB.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(a.bind(`delete from casbin_rule`)); err != nil {
		_ = tx.Rollback()
		return err
	}
	for ptype, ast := range m["p"] {
		for _, rule := range ast.Policy {
			if err := a.insert(tx, ptype, rule); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
	}
	for ptype, ast := range m["g"] {
		for _, rule := range ast.Policy {
			if err := a.insert(tx, ptype, rule); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
	}
	return tx.Commit()
}

// AddPolicy adds one rule.
func (a *Adapter) AddPolicy(_ string, ptype string, rule []string) error {
	return a.insert(a.DB, ptype, rule)
}

// RemovePolicy removes one rule.
func (a *Adapter) RemovePolicy(_ string, ptype string, rule []string) error {
	q := `delete from casbin_rule where ptype = ?`
	args := []any{ptype}
	for i, s := range rule {
		q += fmt.Sprintf(` and v%d = ?`, i)
		args = append(args, s)
	}
	_, err := a.DB.Exec(a.bind(q), args...)
	return err
}

// RemoveFilteredPolicy removes the rules of ptype that match the values from fieldIndex on.
func (a *Adapter) RemoveFilteredPolicy(_ string, ptype string, fieldIndex int, fieldValues ...string) error {
	q := `delete from casbin_rule where ptype = ?`
	args := []any{ptype}
	for i, s := range fieldValues {
		if len(s) == 0 {
			continue
		}
		q += fmt.Sprintf(` and v%d = ?`, fieldIndex+i)
		args = append(args, s)
	}
	_, err := a.DB.Exec(a.bind(q), args...)
	return err
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func (a *Adapter) insert(ex execer, ptype string, rule []string) error {
	var v [6]string
	for i, s := range rule {
		if i >= 6 {
			return fmt.Errorf("a rule of %d columns: casbin_rule holds six", len(rule))
		}
		v[i] = s
	}
	_, err := ex.Exec(a.bind(`insert into casbin_rule (ptype, v0, v1, v2, v3, v4, v5) values (?, ?, ?, ?, ?, ?, ?)`),
		ptype, v[0], v[1], v[2], v[3], v[4], v[5])
	return err
}
