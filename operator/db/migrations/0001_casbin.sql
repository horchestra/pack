-- the policy casbin keeps (К7): ptype and up to six columns of a rule
create table if not exists casbin_rule (
    ptype text not null,
    v0 text not null default '',
    v1 text not null default '',
    v2 text not null default '',
    v3 text not null default '',
    v4 text not null default '',
    v5 text not null default ''
);
create index if not exists casbin_rule_ptype on casbin_rule (ptype);
