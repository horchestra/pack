-- the projects the API wrote (К8: the file is the source at start, PUT /api/projects writes the database)
create table if not exists operator_projects (
    id text primary key,
    body text not null,
    updated_at timestamp not null default current_timestamp
);
