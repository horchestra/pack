-- the operator's own secrets (git credentials first), encrypted by the key of encryption_key_env (К5)
create table if not exists operator_secrets (
    name text primary key,
    blob text not null,
    updated_at timestamptz not null default current_timestamp
);
