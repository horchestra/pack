-- the Bus (К3): the events, the groups' positions and claims, the leaders, the idempotent requests
create table if not exists bus_events (
    seq integer primary key autoincrement,
    kind text not null,
    body text not null,
    at timestamp not null default current_timestamp
);
create table if not exists bus_positions (
    grp text primary key,
    seq integer not null default 0
);
create table if not exists bus_claims (
    grp text not null,
    seq integer not null,
    owner text not null,
    expires_at timestamp not null,
    primary key (grp, seq)
);
create table if not exists bus_leaders (
    name text primary key,
    owner text not null,
    expires_at timestamp not null
);
create table if not exists bus_requests (
    key text primary key,
    id text not null unique,
    body text not null,
    at timestamp not null default current_timestamp
);
