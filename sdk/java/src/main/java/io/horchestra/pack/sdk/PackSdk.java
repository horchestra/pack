package io.horchestra.pack.sdk;

import java.io.IOException;
import java.io.UncheckedIOException;
import java.lang.reflect.Field;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.TreeMap;
import java.util.TreeSet;
import java.util.stream.Stream;

/**
 * The author's side of pack, in Java. A pack is a class extending {@link Pack}; {@link #generate} writes each pack's
 * directory - schema.json, pack.yaml, templates/ - the artifacts the Go sdk writes, in the same format.
 *
 * <pre>
 * public static void main(String[] args) {
 *     System.exit(PackSdk.main(args, new OpenBao()));
 * }
 * </pre>
 */
public final class PackSdk {
    public static final String ARTIFACT_PACK = "application/vnd.pack.pack.v1+json";
    public static final String ARTIFACT_SCHEMA = "application/vnd.pack.schema.v1+json";
    public static final String ARTIFACT_TEMPLATE = "application/vnd.pack.template.v1";
    public static final String ANNOTATION_IMAGE = "pack.image";
    public static final String SCHEMA_FILE = "schema.json";
    public static final String PACK_FILE = "pack.yaml";
    /** The prefix of the repositories packs are published under: repo/name:version. */
    public static final String DEFAULT_REPO = "packs";
    public static final String TEMPLATES_DIR = "templates";
    public static final String CONTENT_DIR = "content";

    private PackSdk() {
    }

    // ---- the packs a pack holds

    private static List<Pack> holds(Pack p) {
        List<Pack> out = new ArrayList<>();
        for (Reflect.Prop prop : Reflect.props(p.getClass())) {
            if (Reflect.isPack(prop.field().getGenericType())) {
                @SuppressWarnings("unchecked")
                Class<? extends Pack> c = (Class<? extends Pack>) prop.field().getType();
                out.add(Reflect.instance(c));
            }
        }
        return out;
    }

    /** The pack and every pack it holds, each once, the pack first. A pack that holds itself is refused. */
    public static List<Pack> packs(Pack root) {
        List<Pack> out = new ArrayList<>();
        walk(root, new ArrayList<>(), new TreeSet<>(), out);
        return out;
    }

    private static void walk(Pack p, List<String> path, Set<String> seen, List<Pack> out) {
        String name = p.meta().name;
        if (name == null || name.isEmpty()) throw new SchemaException(p.getClass().getSimpleName() + ": meta needs a name");
        if (path.contains(name)) {
            List<String> loop = new ArrayList<>(path);
            loop.add(name);
            throw new SchemaException("pack \"" + name + "\" holds itself: " + String.join(" → ", loop));
        }
        if (!seen.add(name)) return;
        out.add(p);
        List<String> next = new ArrayList<>(path);
        next.add(name);
        for (Pack sub : holds(p)) walk(sub, next, seen, out);
    }

    // ---- the artifact

    /** The pack's schema.json. */
    public static String schema(Pack p) {
        return Json.write(Reflect.schema(p));
    }

    static Map<String, String> annotations(Meta m) {
        Map<String, String> out = new TreeMap<>();
        out.put("pack.name", m.name);
        out.put("pack.version", m.version);
        out.put("org.opencontainers.image.title", m.name);
        out.put("org.opencontainers.image.version", m.version);
        Map<String, String> more = new LinkedHashMap<>();
        more.put("description", m.description);
        more.put("documentation", m.documentation);
        more.put("source", m.source);
        more.put("url", m.url);
        more.put("licenses", m.license);
        more.put("vendor", m.vendor);
        more.put("authors", m.authors);
        more.put("created", m.created);
        more.forEach((k, v) -> {
            if (!v.isEmpty()) out.put("org.opencontainers.image." + k, v);
        });
        m.annotations.forEach((k, v) -> {
            if (out.containsKey(k)) {
                throw new SchemaException("annotation " + k + " is written by the sdk from Meta: set the field, not the annotation");
            }
            out.put(k, v);
        });
        return out;
    }

    /** The pack's content/ directory: every regular file under it, by its name relative to it. A symlink is not
     *  content. The files the fields name must be there. */
    public static Map<String, byte[]> content(Pack p) {
        Path root = Path.of(p.meta().dir.isEmpty() ? "." : p.meta().dir).resolve(CONTENT_DIR);
        Map<String, byte[]> out = new TreeMap<>();
        if (Files.isDirectory(root)) {
            try (Stream<Path> walk = Files.walk(root)) {
                for (Path f : (Iterable<Path>) walk.sorted()::iterator) {
                    if (Files.isSymbolicLink(f) || !Files.isRegularFile(f)) continue;
                    out.put(root.relativize(f).toString().replace('\\', '/'), Files.readAllBytes(f));
                }
            } catch (IOException e) {
                throw new UncheckedIOException(e);
            }
        }
        checkNamed(p, out);
        return out;
    }

    private static void checkNamed(Pack p, Map<String, byte[]> have) {
        Set<String> missing = new TreeSet<>();
        Root root = p.packRoot();
        List<Root.File> files = new ArrayList<>(root.content);
        for (Root.OSInstall os : root.packages.values()) {
            files.addAll(os.repo);
            files.addAll(os.content);
        }
        for (Root.File f : files) {
            for (String name : List.of(f.source, f.template)) {
                if (!name.isEmpty() && !have.containsKey(name)) missing.add(f.path + ": " + name);
            }
        }
        if (!missing.isEmpty()) throw new SchemaException("not in " + CONTENT_DIR + "/: " + String.join(", ", missing));
    }

    static Map<String, Object> packFile(Pack p) { return packFile(p, DEFAULT_REPO); }

    static Map<String, Object> packFile(Pack p, String repo) {
        Meta m = p.meta();
        if (m.name == null || m.name.isEmpty() || m.version == null || m.version.isEmpty()) {
            throw new SchemaException(p.getClass().getSimpleName() + ": meta needs a name and a version");
        }
        List<Object> items = new ArrayList<>();
        if (m.image.contains("://")) {
            throw new SchemaException(m.name + ": image \"" + m.image + "\": a repository of the registry the pack is pushed to, repo:tag, no scheme");
        }
        if (!m.image.isEmpty()) {
            Map<String, Object> image = new LinkedHashMap<>();
            image.put("from", "oci://" + m.image);
            image.put("annotations", new TreeMap<>(Map.of(ANNOTATION_IMAGE, "true")));
            items.add(image);
        }
        items.add(item("file://" + SCHEMA_FILE, ARTIFACT_SCHEMA));
        if (!content(p).isEmpty()) items.add(item("dir://" + TEMPLATES_DIR + "/", ARTIFACT_TEMPLATE));
        List<Pack> all = packs(p);
        for (Pack sub : all.subList(1, all.size())) {
            Meta sm = sub.meta();
            if (sm.version.isEmpty()) throw new SchemaException(sm.name + ": meta needs a version");
            items.add(item("oci://" + repo + "/" + sm.name + ":" + sm.version, null));
        }
        Map<String, Object> out = new LinkedHashMap<>();
        out.put("repository", repo + "/" + m.name + ":" + m.version);
        out.put("type", ARTIFACT_PACK);
        out.put("annotations", annotations(m));
        out.put("items", items);
        return out;
    }

    private static Map<String, Object> item(String from, String type) {
        Map<String, Object> out = new LinkedHashMap<>();
        out.put("from", from);
        if (type != null) out.put("type", type);
        return out;
    }

    /** The pack's pack.yaml. */
    public static String packFileYaml(Pack p) {
        return Yaml.dump(packFile(p));
    }

    public static String packFileYaml(Pack p, String repo) {
        return Yaml.dump(packFile(p, repo));
    }

    /** Write every pack's directory under dir: name/schema.json, name/pack.yaml, name/templates/*. The packs a pack
     *  holds are written too, so one call writes the whole tree oci-packer packs. */
    public static void generate(Path dir, Pack... roots) {
        Map<String, Pack> all = new TreeMap<>();
        for (Pack r : roots) {
            for (Pack p : packs(r)) all.putIfAbsent(p.meta().name, p);
        }
        generateIn(dir, DEFAULT_REPO, all.values().toArray(new Pack[0]));
    }

    /** Write the directories of these packs and of no other, published under repo. A pack is generated and published
     *  on its own, the packs it holds first. */
    public static void generateIn(Path dir, String repo, Pack... packs) {
        try {
            for (Pack p : packs) {
                String name = p.meta().name;
                Path out = dir.resolve(name);
                Files.createDirectories(out);
                String schema;
                try {
                    schema = schema(p);
                } catch (SchemaException ex) {
                    throw new SchemaException(name + ": " + ex.getMessage());
                }
                Files.write(out.resolve(SCHEMA_FILE), schema.getBytes(StandardCharsets.UTF_8));
                Files.write(out.resolve(PACK_FILE), packFileYaml(p, repo).getBytes(StandardCharsets.UTF_8));
                for (Map.Entry<String, byte[]> c : content(p).entrySet()) {
                    Path dst = out.resolve(TEMPLATES_DIR).resolve(c.getKey());
                    Files.createDirectories(dst.getParent());
                    Files.write(dst, c.getValue());
                }
            }
        } catch (IOException e) {
            throw new UncheckedIOException(e);
        }
    }

    // ---- the generator CLI

    /**
     * A ready generator CLI of an author's main:
     * <pre>
     * generate [-o dir] [--repo packs]   write the pack's directory, not of the packs it holds (default .)
     * schema &lt;name&gt;       print one pack's schema.json
     * </pre>
     * The author checks run on the generated directory: {@code pack check <dir>}. Returns the exit code.
     */
    public static int main(String[] args, Pack... roots) {
        String prog = "pack-sdk";
        try {
            Map<String, Pack> byName = new TreeMap<>();
            for (Pack r : roots) {
                for (Pack p : packs(r)) byName.putIfAbsent(p.meta().name, p);
            }
            if (args.length >= 1 && args[0].equals("generate")) {
                Path out = Path.of(".");
                String repo = DEFAULT_REPO;
                for (int i = 1; i < args.length; i++) {
                    if ((args[i].equals("-o") || args[i].equals("--output")) && i + 1 < args.length) out = Path.of(args[++i]);
                    else if (args[i].equals("--repo") && i + 1 < args.length) repo = args[++i];
                    else throw new SchemaException("unknown argument " + args[i]);
                }
                generateIn(out, repo, roots);
                for (Pack r : roots) System.out.println("wrote " + out.resolve(r.meta().name));
                return 0;
            }
            if (args.length == 2 && args[0].equals("schema")) {
                Pack p = byName.get(args[1]);
                if (p == null) {
                    throw new SchemaException("no pack \"" + args[1] + "\" (have " + String.join(", ", byName.keySet()) + ")");
                }
                System.out.print(schema(p));
                return 0;
            }
            throw new SchemaException("usage: " + prog + " generate [-o dir] [--repo packs] | schema <name>");
        } catch (SchemaException | UncheckedIOException e) {
            System.err.println(prog + ": " + e.getMessage());
            return 1;
        }
    }
}
