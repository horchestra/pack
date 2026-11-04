-- the refs the synchronizer has already seen (B4): one row per project and branch
create table if not exists operator_sync (
    project text not null,
    ref text not null,
    sha text not null,
    updated_at timestamp not null default current_timestamp,
    primary key (project, ref)
);
