-- the sessions of the UI (httpOnly cookie) and the API tokens of CI (C9)
create table if not exists sessions (
    id text primary key,
    username text not null,
    csrf text not null,
    created_at timestamptz not null default current_timestamp,
    expires_at timestamptz not null
);
create index if not exists sessions_expires on sessions (expires_at);
create table if not exists api_tokens (
    id text primary key,
    name text not null,
    username text not null,
    hash text not null, -- sha256 of the token; the token itself is shown once, at creation
    created_at timestamptz not null default current_timestamp,
    expires_at timestamptz,
    last_used timestamptz
);
create index if not exists api_tokens_user on api_tokens (username);
