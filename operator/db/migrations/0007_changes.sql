-- the change requests (B9): a branch, its author and title, open | merged | closed | conflict; the plans the
-- branch's commits gave, by release; the comments.
create table if not exists operator_changes (
    id text primary key,
    project text not null,
    branch text not null,
    author text not null,
    title text not null,
    state text not null default 'open',
    sha text not null default '',
    created_at timestamp not null default current_timestamp,
    updated_at timestamp not null default current_timestamp,
    unique (project, branch)
);
create table if not exists operator_change_comments (
    id text primary key,
    change text not null,
    author text not null,
    body text not null,
    at timestamp not null default current_timestamp
);
create table if not exists operator_change_plans (
    change text not null,
    release text not null,
    sha text not null,
    digest text not null,
    plan text not null,
    at timestamp not null default current_timestamp,
    primary key (change, release)
);
