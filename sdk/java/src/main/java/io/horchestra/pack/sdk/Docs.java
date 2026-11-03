package io.horchestra.pack.sdk;

import java.io.IOException;
import java.io.UncheckedIOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;
import java.util.regex.Pattern;
import java.util.stream.Stream;

/**
 * The documentation of a pack's settings, in languages: docs/&lt;lang&gt;.yaml next to the pack's source, keyed by the
 * JSON Pointer of a field, written into the schema the way the Go sdk writes it (sdk/docs) - the base language as
 * title, description and examples, the others as x-pack-i18n. The YAML is the subset the Go sdk reads, read here
 * without a library, and read the same.
 */
final class Docs {
    static final String DIR = "docs";
    static final String DEFAULT_BASE = "en";
    static final String I18N = "x-pack-i18n", GROUP = "x-pack-group", GROUPS = "x-pack-groups", LANGS = "x-pack-langs";

    private static final Pattern LANG = Pattern.compile("^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$");
    private static final Pattern PLAIN_KEY = Pattern.compile("^[^\\s\\-?:,\\[\\]{}#&*!|>'\"%@`]");
    private static final Pattern INTEGER = Pattern.compile("^-?(0|[1-9][0-9]*)$");
    private static final Pattern FRACTION = Pattern.compile("^-?(0|[1-9][0-9]*)\\.[0-9]*[1-9]$");
    /** What a number of examples may be, in every SDK alike (sdk/docs.NumberRule): the text each writes back unchanged. */
    static final String NUMBER_RULE = "an integer of int64, or -?digits.digits: no exponent, no trailing zero, at most 15 significant digits, at least 0.0001";

    private static boolean canonicalFraction(String x) {
        String[] wf = (x.startsWith("-") ? x.substring(1) : x).split("\\.", 2);
        String whole = wf[0], frac = wf[1];
        int zeros = 0;
        while (zeros < frac.length() && frac.charAt(zeros) == '0') zeros++;
        if (whole.equals("0") && zeros > 3) return false;
        String digits = whole + frac;
        int lead = 0;
        while (lead < digits.length() && digits.charAt(lead) == '0') lead++;
        return digits.length() - lead <= 15;
    }

    /** The order Go's jsonschema writes the keys of a node in; the x- keys follow, sorted. */
    private static final List<String> ORDER = Arrays.asList("$schema", "$id", "$anchor", "$ref", "$dynamicRef", "$defs",
            "$comment", "allOf", "anyOf", "oneOf", "not", "if", "then", "else", "dependentSchemas", "prefixItems", "items",
            "contains", "properties", "patternProperties", "additionalProperties", "propertyNames", "type", "enum", "const",
            "multipleOf", "maximum", "exclusiveMaximum", "minimum", "exclusiveMinimum", "maxLength", "minLength", "pattern",
            "maxItems", "minItems", "uniqueItems", "maxContains", "minContains", "maxProperties", "minProperties", "required",
            "dependentRequired", "format", "contentEncoding", "contentMediaType", "contentSchema", "title", "description",
            "default", "deprecated", "readOnly", "writeOnly", "examples");
    private static final List<String> RULE_ORDER = List.of("id", "rule", "message", "phases", I18N);

    private Docs() {
    }

    // ---- the YAML of docs/

    private static final class Parser {
        final String[] lines;
        int i;

        Parser(String src) {
            lines = src.replace("\r\n", "\n").split("\n", -1);
        }

        SchemaException err(String msg) {
            return new SchemaException("line " + (i + 1) + ": " + msg);
        }

        boolean next() {
            for (; i < lines.length; i++) {
                String t = stripLeft(lines[i]);
                if (!t.isEmpty() && t.charAt(0) != '#') return true;
            }
            return false;
        }

        int indent() {
            String l = lines[i];
            int n = l.length() - stripLeft(l).length();
            if (n < l.length() && l.charAt(n) == '\t') throw err("a tab in the indentation");
            return n;
        }

        Map<String, Object> mapping(int indent) {
            Map<String, Object> out = new LinkedHashMap<>();
            while (next()) {
                int n = indent();
                if (n < indent) break;
                if (n > indent) throw err("unexpected indentation");
                String line = lines[i].substring(n);
                if (line.equals("-") || line.startsWith("- ")) throw err("a list item where a key was expected");
                String[] kr = key(line);
                String key = kr[0], rest = kr[1];
                if (out.containsKey(key)) throw err("\"" + key + "\" twice");
                Object v;
                if (rest.isEmpty()) {
                    i++;
                    if (!next()) throw new SchemaException("line " + i + ": \"" + key + "\" has no value");
                    int child = indent();
                    if (child <= indent) throw err("\"" + key + "\" has no value");
                    String first = lines[i].substring(child);
                    v = first.equals("-") || first.startsWith("- ") ? list(child) : mapping(child);
                } else if (rest.equals("|") || rest.equals("|-")) {
                    i++;
                    v = block(indent);
                } else {
                    v = scalar(rest);
                    i++;
                }
                out.put(key, v);
            }
            return out;
        }

        List<String> list(int indent) {
            List<String> out = new ArrayList<>();
            while (next()) {
                int n = indent();
                if (n < indent) break;
                if (n > indent) throw err("unexpected indentation");
                String line = lines[i].substring(n);
                if (!line.startsWith("- ")) {
                    throw err(line.equals("-") ? "a list item without a value" : "a key where a list item was expected");
                }
                String rest = stripLeft(line.substring(2));
                if (rest.equals("|") || rest.equals("|-")) throw err("a block in a list: write the item on its line");
                out.add(scalar(rest));
                i++;
            }
            return out;
        }

        String block(int indent) {
            List<String> out = new ArrayList<>();
            int depth = -1;
            for (; i < lines.length; i++) {
                String l = lines[i];
                if (stripLeft(l).isEmpty()) {
                    out.add("");
                    continue;
                }
                int n = l.length() - stripLeft(l).length();
                if (depth < 0) {
                    if (n <= indent) break;
                    depth = n;
                }
                if (n < depth) break;
                out.add(l.substring(depth));
            }
            while (!out.isEmpty() && out.getLast().isEmpty()) out.removeLast();
            return String.join("\n", out);
        }

        String[] key(String line) {
            if (line.charAt(0) == '"') {
                String[] q = doubleQuoted(line);
                if (!q[1].startsWith(":")) throw err("a quoted key without ':'");
                return new String[]{q[0], value(q[1].substring(1))};
            }
            if (!PLAIN_KEY.matcher(line).find() && !line.startsWith("/")) throw err("a key that is not plain: quote it");
            int at = line.indexOf(": ");
            if (at >= 0) return new String[]{stripRight(line.substring(0, at)), value(line.substring(at + 1))};
            if (line.endsWith(":")) return new String[]{stripRight(line.substring(0, line.length() - 1)), ""};
            throw err("no ':' after the key");
        }

        String scalar(String s) {
            char c = s.charAt(0);
            if (c == '"') {
                String[] q = doubleQuoted(s);
                tail(q[1]);
                return q[0];
            }
            if (c == '\'') {
                String[] q = singleQuoted(s);
                tail(q[1]);
                return q[0];
            }
            if ("[{&*!>|%@`".indexOf(c) >= 0) {
                throw err("\"" + s + "\": not in the YAML of docs/ (block style and plain or quoted scalars only)");
            }
            int at = s.indexOf(" #");
            if (at >= 0) s = s.substring(0, at);
            s = stripRight(s);
            if (s.contains(": ") || s.endsWith(":")) throw err("\"" + s + "\": a ':' in a plain scalar - quote it");
            return s;
        }

        void tail(String after) {
            after = stripLeft(after);
            if (!after.isEmpty() && after.charAt(0) != '#') throw err("\"" + after + "\" after a quoted scalar");
        }

        String[] doubleQuoted(String s) {
            StringBuilder b = new StringBuilder();
            for (int k = 1; k < s.length(); k++) {
                char c = s.charAt(k);
                if (c == '"') return new String[]{b.toString(), s.substring(k + 1)};
                if (c != '\\') {
                    b.append(c);
                    continue;
                }
                if (k + 1 >= s.length()) throw err("an escape at the end of the line");
                char e = s.charAt(++k);
                switch (e) {
                    case '"', '\\', '/' -> b.append(e);
                    case 'n' -> b.append('\n');
                    case 't' -> b.append('\t');
                    case 'r' -> b.append('\r');
                    case 'u' -> {
                        if (k + 4 >= s.length()) throw err("a short \\u escape");
                        String h = s.substring(k + 1, Math.min(k + 5, s.length()));
                        if (!h.matches("[0-9A-Fa-f]{4}")) throw err("\\u" + h + ": not hex");
                        b.append((char) Integer.parseInt(h, 16));
                        k += 4;
                    }
                    default -> throw err("\\" + e + ": not an escape of docs/");
                }
            }
            throw err("a double-quoted scalar ends on its line");
        }

        String[] singleQuoted(String s) {
            StringBuilder b = new StringBuilder();
            for (int k = 1; k < s.length(); k++) {
                if (s.charAt(k) == '\'') {
                    if (k + 1 < s.length() && s.charAt(k + 1) == '\'') {
                        b.append('\'');
                        k++;
                        continue;
                    }
                    return new String[]{b.toString(), s.substring(k + 1)};
                }
                b.append(s.charAt(k));
            }
            throw err("a single-quoted scalar ends on its line");
        }
    }

    private static String stripLeft(String s) {
        int n = 0;
        while (n < s.length() && s.charAt(n) == ' ') n++;
        return s.substring(n);
    }

    private static String stripRight(String s) {
        int n = s.length();
        while (n > 0 && s.charAt(n - 1) == ' ') n--;
        return s.substring(0, n);
    }

    private static String value(String s) {
        s = stripRight(stripLeft(s));
        return s.startsWith("#") ? "" : s;
    }

    /** A document of the subset: a mapping at its top; every scalar a string. */
    static Map<String, Object> parse(String src) {
        Parser p = new Parser(src);
        Map<String, Object> out = p.mapping(0);
        if (p.next()) throw p.err("unexpected indentation");
        return out;
    }

    // ---- the files

    record Entry(String title, String description, List<String> examples, String group) {
    }

    static final class File {
        final String lang;
        boolean base;
        final Map<String, Entry> fields = new LinkedHashMap<>();
        Map<String, String> groups = new LinkedHashMap<>();
        Map<String, String> rules = new LinkedHashMap<>();

        File(String lang) {
            this.lang = lang;
        }
    }

    @SuppressWarnings("unchecked")
    static File parseFile(String lang, String src) {
        File f = new File(lang);
        for (Map.Entry<String, Object> kv : parse(src).entrySet()) {
            String k = kv.getKey();
            Object v = kv.getValue();
            if (k.equals("base")) {
                if (!"true".equals(v)) throw new SchemaException("base: true or nothing");
                f.base = true;
            } else if (k.equals("groups") || k.equals("rules")) {
                if (!(v instanceof Map<?, ?> m) || !m.values().stream().allMatch(x -> x instanceof String)) {
                    throw new SchemaException(k + ": a mapping of an id to its text");
                }
                if (k.equals("groups")) f.groups = (Map<String, String>) m;
                else f.rules = (Map<String, String>) m;
            } else if (k.startsWith("/")) {
                f.fields.put(k, entry(k, v));
            } else {
                throw new SchemaException("\"" + k + "\": a key is a JSON Pointer of a field (/…, / the pack), base, groups or rules");
            }
        }
        return f;
    }

    @SuppressWarnings("unchecked")
    private static Entry entry(String ptr, Object v) {
        if (!(v instanceof Map<?, ?> m)) throw new SchemaException(ptr + ": a mapping of title, description, examples, group");
        String title = "", description = "", group = "";
        List<String> examples = List.of();
        for (Map.Entry<String, Object> kv : ((Map<String, Object>) m).entrySet()) {
            String k = kv.getKey();
            Object x = kv.getValue();
            switch (k) {
                case "title", "description", "group" -> {
                    if (!(x instanceof String s)) throw new SchemaException(ptr + ": " + k + ": a text");
                    if (k.equals("title")) {
                        if (s.contains("\n")) throw new SchemaException(ptr + ": title: one line");
                        title = s;
                    } else if (k.equals("description")) {
                        description = s;
                    } else {
                        group = s;
                    }
                }
                case "examples" -> {
                    if (!(x instanceof List<?> l)) throw new SchemaException(ptr + ": examples: a list");
                    examples = (List<String>) l;
                }
                default -> throw new SchemaException(ptr + ": \"" + k + "\": a field has title, description, examples and group");
            }
        }
        return new Entry(title, description, examples, group);
    }

    /** The base language and the others (sorted) of dir/docs/; null when the pack has no docs/. */
    static List<File> load(Path dir) {
        Path root = dir.resolve(DIR);
        if (!Files.isDirectory(root)) return null;
        List<File> files = new ArrayList<>();
        try (Stream<Path> s = Files.list(root)) {
            for (Path path : (Iterable<Path>) s.sorted()::iterator) {
                String name = path.getFileName().toString();
                if (Files.isDirectory(path) || name.startsWith(".")) continue;
                String lang = name.endsWith(".yaml") ? name.substring(0, name.length() - 5) : "";
                if (!LANG.matcher(lang).matches()) {
                    throw new SchemaException(DIR + "/" + name + ": a file of " + DIR + "/ is <lang>.yaml (en.yaml, ru.yaml, pt-BR.yaml)");
                }
                try {
                    files.add(parseFile(lang, Files.readString(path, StandardCharsets.UTF_8)));
                } catch (SchemaException e) {
                    throw new SchemaException(DIR + "/" + name + ": " + e.getMessage());
                }
            }
        } catch (IOException e) {
            throw new UncheckedIOException(e);
        }
        if (files.isEmpty()) return null;
        List<File> bases = files.stream().filter(f -> f.base).toList();
        if (bases.size() > 1) {
            throw new SchemaException(DIR + "/: " + bases.get(0).lang + ".yaml and " + bases.get(1).lang + ".yaml both say base: true");
        }
        if (bases.isEmpty()) bases = files.stream().filter(f -> f.lang.equals(DEFAULT_BASE)).toList();
        if (bases.isEmpty()) {
            throw new SchemaException(DIR + "/: no base language: write " + DEFAULT_BASE + ".yaml, or say base: true in one file");
        }
        File base = bases.getFirst();
        base.base = true;
        List<File> out = new ArrayList<>();
        out.add(base);
        List<File> others = new ArrayList<>();
        for (File f : files) {
            if (f == base) continue;
            f.fields.forEach((ptr, e) -> {
                if (!e.examples().isEmpty() || !e.group().isEmpty()) {
                    throw new SchemaException(DIR + "/" + f.lang + ".yaml: " + ptr + ": examples and group are the base language's (" + base.lang + ".yaml)");
                }
            });
            for (String gid : f.groups.keySet()) {
                if (!base.groups.containsKey(gid)) {
                    throw new SchemaException(DIR + "/" + f.lang + ".yaml: groups: \"" + gid + "\" is not a group of " + base.lang + ".yaml");
                }
            }
            others.add(f);
        }
        others.sort((a, b) -> a.lang.compareTo(b.lang));
        out.addAll(others);
        return out;
    }

    // ---- the schema

    @SuppressWarnings("unchecked")
    static Map<String, Object> lookup(Map<String, Object> root, String ptr) {
        if (ptr.equals("/")) return root;
        Map<String, Object> node = root;
        for (String seg : ptr.substring(1).split("/", -1)) {
            seg = seg.replace("~1", "/").replace("~0", "~");
            if (node.containsKey("$ref")) throw new SchemaException("a field of a held pack: it is documented in that pack's " + DIR + "/");
            Object props = node.get("properties");
            if (props == null) throw new SchemaException("\"" + seg + "\": not an object");
            Object next = ((Map<String, Object>) props).get(seg);
            if (next == null) throw new SchemaException("\"" + seg + "\": no such field");
            node = (Map<String, Object>) next;
        }
        return node;
    }

    /** The pack and its fields, depth first in the order of the schema; not into a held pack. */
    @SuppressWarnings("unchecked")
    static void walk(Map<String, Object> node, List<Map<String, Object>> out) {
        out.add(node);
        if (node.containsKey("$ref")) return;
        Object props = node.get("properties");
        if (props instanceof Map<?, ?> m) {
            for (Object child : m.values()) walk((Map<String, Object>) child, out);
        }
    }

    private static List<Object> examples(Map<String, Object> node, List<String> xs) {
        if (node.containsKey("$ref") || "password".equals(node.get("format")) || Boolean.TRUE.equals(node.get("writeOnly"))) {
            throw new SchemaException("a held pack and a secret have no examples");
        }
        List<Object> out = new ArrayList<>();
        Object t = node.get("type");
        for (String x : xs) {
            if ("string".equals(t)) {
                out.add(x);
            } else if ("integer".equals(t) || "number".equals(t)) {
                if (INTEGER.matcher(x).matches()) {
                    try {
                        out.add(Long.parseLong(x));
                    } catch (NumberFormatException e) {
                        throw new SchemaException("\"" + x + "\": " + NUMBER_RULE);
                    }
                } else if ("number".equals(t) && FRACTION.matcher(x).matches() && canonicalFraction(x)) {
                    out.add(Double.parseDouble(x)); // Json writes it back as x
                } else {
                    throw new SchemaException("\"" + x + "\": " + NUMBER_RULE);
                }
            } else if ("boolean".equals(t)) {
                if (!x.equals("true") && !x.equals("false")) throw new SchemaException("\"" + x + "\" is not true or false");
                out.add(x.equals("true"));
            } else {
                throw new SchemaException("only a string, a number or a boolean has examples here");
            }
        }
        return out;
    }

    /** Write the documentation into a pack's schema, as the Go sdk's docs.Apply does, in its order of keys. */
    @SuppressWarnings("unchecked")
    static void apply(Map<String, Object> root, List<File> files) {
        File base = files.getFirst();
        List<File> others = files.subList(1, files.size());
        for (File f : files) {
            for (Map.Entry<String, Entry> kv : f.fields.entrySet()) {
                String ptr = kv.getKey();
                Entry e = kv.getValue();
                try {
                    Map<String, Object> node = lookup(root, ptr);
                    if (f.base) {
                        if (!e.title().isEmpty()) node.put("title", e.title());
                        if (!e.description().isEmpty()) node.put("description", e.description());
                        if (!e.examples().isEmpty()) node.put("examples", examples(node, e.examples()));
                        if (!e.group().isEmpty()) node.put(GROUP, e.group());
                    } else {
                        Map<String, Object> t = new TreeMap<>();
                        if (!e.title().isEmpty()) t.put("title", e.title());
                        if (!e.description().isEmpty()) t.put("description", e.description());
                        if (!t.isEmpty()) ((Map<String, Object>) node.computeIfAbsent(I18N, k -> new TreeMap<>())).put(f.lang, t);
                    }
                    canonical(node, ORDER);
                } catch (SchemaException err) {
                    throw new SchemaException(DIR + "/" + f.lang + ".yaml: " + ptr + ": " + err.getMessage());
                }
            }
        }
        List<Map<String, Object>> nodes = new ArrayList<>();
        walk(root, nodes);
        List<String> used = new ArrayList<>();
        for (Map<String, Object> n : nodes) {
            Object g = n.get(GROUP);
            if (g instanceof String s && !used.contains(s)) used.add(s);
        }
        List<Object> groups = new ArrayList<>();
        for (String gid : used) {
            String title = base.groups.getOrDefault(gid, "");
            if (title.isEmpty()) throw new SchemaException(DIR + "/" + base.lang + ".yaml: group \"" + gid + "\" has no title under groups:");
            Map<String, Object> g = new TreeMap<>();
            g.put("id", gid);
            g.put("title", title);
            Map<String, Object> tr = new TreeMap<>();
            for (File f : others) {
                String t = f.groups.getOrDefault(gid, "");
                if (!t.isEmpty()) tr.put(f.lang, new TreeMap<>(Map.of("title", t)));
            }
            if (!tr.isEmpty()) g.put(I18N, tr);
            groups.add(g);
        }
        for (String gid : base.groups.keySet()) {
            if (!used.contains(gid)) throw new SchemaException(DIR + "/" + base.lang + ".yaml: group \"" + gid + "\": no field is in it");
        }
        if (!groups.isEmpty()) root.put(GROUPS, groups);
        Map<String, Map<String, Object>> byId = new LinkedHashMap<>();
        for (Map<String, Object> n : nodes) {
            Object rs = n.get("x-pack-rules");
            if (rs instanceof List<?> l) {
                for (Object r : l) {
                    Map<String, Object> rm = (Map<String, Object>) r;
                    if (rm.get("id") instanceof String id && !id.isEmpty()) byId.put(id, rm);
                }
            }
        }
        for (File f : files) {
            for (Map.Entry<String, String> kv : f.rules.entrySet()) {
                Map<String, Object> r = byId.get(kv.getKey());
                if (r == null) {
                    throw new SchemaException(DIR + "/" + f.lang + ".yaml: rules: \"" + kv.getKey() + "\" is not the id of a rule of the pack");
                }
                if (kv.getValue().isEmpty()) continue;
                if (f.base) {
                    r.put("message", kv.getValue());
                } else {
                    ((Map<String, Object>) r.computeIfAbsent(I18N, k -> new TreeMap<>())).put(f.lang, new TreeMap<>(Map.of("message", kv.getValue())));
                }
                canonical(r, RULE_ORDER);
            }
        }
        List<String> langs = new ArrayList<>();
        for (File f : files) langs.add(f.lang);
        root.put(LANGS, langs);
        canonical(root, ORDER);
    }

    /** Puts the keys of a map in the order given, then the x- keys sorted, then the rest as they were. */
    private static void canonical(Map<String, Object> m, List<String> order) {
        Map<String, Object> copy = new LinkedHashMap<>(m);
        m.clear();
        for (String k : order) {
            if (copy.containsKey(k)) m.put(k, copy.remove(k));
        }
        for (Map.Entry<String, Object> e : copy.entrySet()) {
            if (!e.getKey().startsWith("x-")) m.put(e.getKey(), e.getValue());
        }
        for (Map.Entry<String, Object> e : new TreeMap<>(copy).entrySet()) {
            if (e.getKey().startsWith("x-")) m.put(e.getKey(), e.getValue());
        }
    }
}
