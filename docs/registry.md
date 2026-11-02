# Packs and registries

A pack travels as an OCI artifact. `oci-packer` (`github.com/arenadata/oci-packer`) does all the registry work; `pack`
links only its types. A registry holds three kinds of thing:

|        |                                                                                                                                        |
|--------|----------------------------------------------------------------------------------------------------------------------------------------|
| images | what the application runs: `images/openbao:2.6.5`; `pack` never reads their layers                                                     |
| packs  | the artifact of a pack: its pack file, `schema.json`, `templates/`, and the packs it holds mounted by reference: `packs/openbao:2.6.5` |

## Publishing

1. **Generate** the pack's directory with the SDK (`generate`, see [authoring.md](authoring.md)): `<name>/schema.json`,
   `<name>/pack.yaml`, `<name>/templates/`, for the pack and every pack it holds.
2. **Put the images** into the registry the packs go to (`oci-packer copy` from where they are).
3. **Push**, the packs a pack holds first, then the pack:

   ```
   (cd tembo   && oci-pack oci://registry.example/packs/tembo:0.2.0   -f pack.yaml)
   (cd openbao && oci-pack oci://registry.example/packs/openbao:2.6.5 -f pack.yaml)
   ```

The order is not a convention: an `oci://repo:tag` item of a pack file names **no registry**; it is mounted from the
registry being pushed to, so the image and every held pack must already be there. `pack.yaml` is
the pack file: `Meta` as annotations (`pack.name`, `pack.version` and the `org.opencontainers.image.*` ones), the image
(marked `pack.image`), the schema, the templates (`dir://templates/`) and the artifact of each held pack (its
`Meta.Artifact:Version`). When a pack is extracted, an index that mixes an image with built files is unpacked; an index
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

## pack init

```
pack init oci://registry.example/packs/openbao:2.6.5
```

resolves the tag to a **digest** and extracts the pack, with the packs mounted in it, into
`.pack/packs/<digest>/<name>/`. The digest goes into `release.lock.json`:

```json
{
  "pack": "oci://registry.example/packs/openbao:2.6.5",
  "digest": "sha256:4f3e…"
}
```

Every other command reads that directory and never the registry, so **a release gives the same result whatever the tag
becomes**: commit the lock. `pack init` with a lock present reads the locked digest; `--upgrade` takes what the tag
means now and rewrites the lock. A plain-HTTP registry (`localhost:5001`) needs `registry: {plain_http: true}` in
`env.yaml`.

The units of a release carry the images as the pack names them — `images/openbao:2.6.5`, no host; the target adds the
registry's host when it deploys.
