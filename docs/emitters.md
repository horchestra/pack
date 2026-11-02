# Emitters: the formats of a file

An emitter turns a values subtree into the bytes of a file (`file:"/path" format:"…"`). Every format but `template` is
serialisation — it cannot produce a malformed file; keys are sorted; nothing depends on the clock. Rendering the same
subtree twice gives the same bytes, which is what lets a Secret be named by its content.

| format       | rule                                                                                                                                                                                              | example                        |
|--------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|--------------------------------|
| `json`       | two-space indent, sorted keys                                                                                                                                                                     | —                              |
| `yaml`       | block style, sorted keys                                                                                                                                                                          | —                              |
| `properties` | nested objects flattened with dots; lists joined by commas; **keys** escape `=`, `:`, space, `#`, `!`; **values** escape only `\`, line breaks and a leading space, so `host:port` stays readable | `server.1=h1:2888:3888`        |
| `env`        | flattened with `_`, upper-cased, `-` and `.` → `_`; quoted when the value has spaces or quotes                                                                                                    | `LOG_DIRS=/var/lib/kafka`      |
| `shell`      | `env` with `export ` in front and single-quoting for anything the shell would interpret — a file that is *sourced*                                                                                | `export HADOOP_OPTS='-Djava…'` |
| `ini`        | objects are sections; an object inside a section is a nested section (`[[x]]`), the way configobj reads it; scalars before the first section are global                                           | `[desktop]\n[[database]]`      |
| `flagfile`   | `--<key>=<value>`, one leaf per line, key verbatim; `true`/`false`, decimals, comma-joined lists; an absent value — no line; nesting and line breaks are refused                                  | `--mem_limit=52428m`           |
| `lines`      | a list of scalars, one per line (a scalar alone is one line); an item with a line break is refused                                                                                                | `-server\n-Xmx1G`              |
| `hadoop-xml` | `<configuration><property><name/><value/></property>…`, one level, XML-escaped                                                                                                                    | HDFS, Hive, HBase              |
| `toml`       | tables for nested objects, sorted keys, whole numbers as integers; null is refused                                                                                                                | `[db]\nhost = 'pg'`            |
| `text`       | a string leaf as is, newline-terminated                                                                                                                                                           | `myid`                         |
| `template`   | Go `text/template` with sprig and Helm's functions; the result is parsed back as `parseAs`                                                                                                        | JAAS, log4j, `config.hcl`      |
| `raw`        | the pack's `content/<template>`, byte for byte: no template language — a script, a dump, an archive                                                                                               | `entrypoint.sh`                |
| `secret`     | on a password leaf: the referenced Secret's key is mounted at the path, bytes untouched — a keytab, a keystore, a certificate. Nothing is rendered                                                | —                              |

The format is the extension's when no `format` tag says it: `.json`, `.yaml`/`.yml`, `.properties`, `.toml`,
`.ini`/`.cnf`, `.env`. A file with a `template` tag and no format is `template`.

## Template data

For a file whose node is an object, the object's fields are the top-level data; for a scalar node, it is under `value`.
In both cases `values` (the whole document) and `pack` (the context of [values.md](values.md)) are available:

```
username="{{ .username }}"            a field of the subtree
{{ .values.openbao.api_addr }}        any value of the document
{{ .pack.installation }}              the context
```

## Template functions

A template has what a Helm chart has:

- all of [sprig](https://masterminds.github.io/sprig/) — except `env` and `expandenv`: a file rendered from a pack must
  not depend on the machine that renders it;
- `toYaml`, `toJson`, `toPrettyJson`, `toToml` and `fromYaml`, `fromJson`, `fromToml`, `fromYamlArray`, `fromJsonArray`
  (and `mustToYaml`, `mustToJson`);
- `required "message" value`, `include "name" data` (a `{{ define }}` of the same file), `tpl "text" data`;
- `lookup` answers an empty map, as `helm template` does: the render knows no cluster.

A key the data does not have is nil, so `default` and `required` can answer for it. If it reaches the result as
`<no value>`, the template fails with the line. A template that calls `now`, `randAlphaNum`, `uuidv4` … is the author's
to answer for; every other format gives the same bytes for the same values.

## Which format for which application

| application                                                      | format                              |
|------------------------------------------------------------------|-------------------------------------|
| Java `.properties`, Kafka, ZooKeeper; Postgres `postgresql.conf` | `properties`                        |
| gflags binaries (Impala)                                         | `flagfile`                          |
| Hadoop, Hive, HBase, Ranger `*-site.xml`                         | `hadoop-xml`                        |
| ClickHouse; TOML configs                                         | `yaml`; `toml`                      |
| `EnvironmentFile`; sourced shell files (`hadoop-env.sh`)         | `env`; `shell`                      |
| `.ini`, configobj (`hue.ini`); one flag per line (`jvm.config`)  | `ini`; `lines`                      |
| one value in a file                                              | `text`                              |
| keytabs, keystores, certificates                                 | `secret` (a mount, not a rendering) |
| anything else                                                    | `template` + `parseAs`              |
