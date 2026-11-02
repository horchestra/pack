#!/bin/sh
# The first start on a host: no image's entrypoint made the role and the database. Run as a postInstall hook, so it may
# run twice.
set -eu
i=0
until runuser -u postgres -- pg_isready -q; do
    i=$((i + 1))
    [ "$i" -lt 60 ] || { echo "postgres did not start" >&2; exit 1; }
    sleep 1
done
runuser -u postgres -- psql -v ON_ERROR_STOP=1 --dbname postgres <<'SQL'
SELECT 'CREATE ROLE "{{ .superuser.name }}" SUPERUSER LOGIN' WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '{{ .superuser.name }}')\gexec
ALTER ROLE "{{ .superuser.name }}" PASSWORD '{{ .superuser.password }}';
SELECT 'CREATE DATABASE "{{ .database }}"' WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = '{{ .database }}')\gexec
{{- range .init_extensions }}
CREATE EXTENSION IF NOT EXISTS {{ . }};
{{- end }}
SQL
