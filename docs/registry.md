# Packs and registries

A pack travels as an OCI artifact. `oci-packer` (`github.com/arenadata/oci-packer`) does all the registry work; `pack`
links only its types. A registry holds three kinds of thing:

|        |                                                                                                                                        |
|--------|----------------------------------------------------------------------------------------------------------------------------------------|
| images | what the application runs: `images/openbao:2.6.5`; `pack` never reads their layers                                                     |
| packs  | the artifact of a pack: its pack file, `schema.json`, `templates/`, and the packs it holds mounted by reference: `packs/openbao:2.6.5` |

## Publishing

A pack is generated and published on its own, the packs it holds first:

```
go run ./postgres/cmd generate -o out
oci-packer oci://registry.example -f out/postgres/pack.yaml

go run ./openbao/cmd generate -o out
oci-packer oci://registry.example -f out/openbao/pack.yaml
```

Before that, **put the images** into the registry the packs go to (`oci-packer copy` from where they are).

`generate` writes the directory of the pack, not of the packs it holds: `<name>/schema.json`, `<name>/pack.yaml`,
`<name>/templates/`. With one `-o` the directories lie side by side, which is what `pack check` reads.

**Where a pack goes has one source.** `pack.yaml` says it, `repository: packs/openbao:2.6.5`, and the command names only
the registry; a command that names another repository is refused. The repository is `<repo>/<Name>:<Version>`, `repo`
being `packs` unless `generate --repo` says otherwise; the packs a pack holds are named under the same `repo`.

The order is not a convention: an `oci://repo:tag` item of a pack file names **no registry**; it is mounted from the
registry being pushed to, so the image and every held pack must already be there. `pack.yaml` is
the pack file: `repository`, `Meta` as annotations (`pack.name`, `pack.version` and the `org.opencontainers.image.*` ones),
the image (marked `pack.image`), the schema, the templates (`dir://templates/`) and each held pack.
When a pack is extracted, an index that mixes an image with built files is unpacked; an index
of images with
build attestations is an image, and stays a reference.

## Reading

`oci-packer get oci://host/repo:tag:path/in/the/pack` reads one file of a pack without extracting it.

## Publishing a target

A target's program is published the same way, as an artifact with one file, `pack-target-<name>`, one tag per platform
(`{os}` and `{arch}` of the source line stand for GOOS and GOARCH):

```
# pack.yaml
type: application/vnd.pack.target.v1
items:
- from: file://pack-target-docker
```

```
oci-pack oci://registry.example/targets/docker:0.1.0-linux-amd64 -f pack.yaml
```

and `release.yaml` names it: `targets: {docker: oci://registry.example/targets/docker:0.1.0-{os}-{arch}}`.

## pack init and pack refresh

```
pack init oci://registry.example/packs/openbao:2.6.5 prod --target docker
```

makes the release (see [commands.md](commands.md)), resolves the tag to a **digest** and extracts the pack, with the
packs mounted in it, into `.pack/packs/<digest>/<name>/`. The digest goes into `release.lock.json`:

```json
{
  "pack": "oci://registry.example/packs/openbao:2.6.5",
  "digest": "sha256:4f3e…"
}
```

Every other command reads that directory and never the registry, so **a release gives the same result whatever the tag
becomes**: commit the lock. `pack refresh` brings what the release names — after a clone, or a change of
`release.yaml` — reading the locked digest; `--upgrade` takes what the tag means now and rewrites the lock. A plain-HTTP registry (`localhost:5001`) needs `registry: {plain_http: true}` in
`release.yaml`; a target from a plain-HTTP registry says so in its own source
(`targets: {docker: {url: oci://…, plain_http: true}}`).

The units of a release carry the images as full references — a mounted member is named by where it pulls from,
`<host>/packs/openbao@sha256:…` — so the engine of any target takes them as they are.
