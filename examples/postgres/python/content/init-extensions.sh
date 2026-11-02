#!/bin/sh
# The extensions every database gets, after the first start: run as a postInstall hook, so it may run twice.
set -eu
export PGPASSWORD="$POSTGRES_PASSWORD"
psql -v ON_ERROR_STOP=1 --host {{ .pack.service }} --username "$POSTGRES_USER" --dbname postgres <<'SQL'
{{- range .init_extensions }}
CREATE EXTENSION IF NOT EXISTS {{ . }};
{{- end }}
SQL
