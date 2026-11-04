-- the policy's projections (B5): jobs as they stand, requests and jobs by release
create table if not exists bus_jobs (
    id text primary key,
    body text not null,
    updated_at timestamp not null default current_timestamp
);
create table if not exists operator_requests (
    id text primary key,
    repo text not null,
    release text not null,
    sha text not null,
    action text not null,
    digest text not null default '',
    state text not null,
    body text not null,
    updated_at timestamp not null default current_timestamp
);
create index if not exists operator_requests_release on operator_requests (repo, release, state);
create table if not exists operator_jobs (
    id text primary key,
    request text not null,
    repo text not null,
    release text not null,
    how text not null,
    mutating integer not null default 0,
    state text not null,
    body text not null,
    updated_at timestamp not null default current_timestamp
);
create index if not exists operator_jobs_release on operator_jobs (repo, release, state);
