#!/bin/sh
# The compensation of the major-version upgrade: what pg_upgrade did to the data directory is thrown away and the
# old cluster starts again as it was.
set -eu
rm -rf /var/lib/postgresql/data.16
