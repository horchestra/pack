package io.horchestra.pack.sdk;

import java.util.ArrayList;
import java.util.Comparator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.regex.Pattern;

/** One of x-pack-actions on the root of a pack: an operation the author names, pack run performs on a key of the pack
 *  with the installed image. The words of command are templates over the parameters ({{ .to }}); mutating — it changes
 *  the installation; approve — never run unasked but under run.approve: auto. */
public final class Action {
    static final List<String> TYPES = List.of("string", "integer", "number", "boolean");
    static final Pattern NAME = Pattern.compile("^[a-z][a-z0-9-]*$"), PARAM = Pattern.compile("^[a-z][a-z0-9_]*$");

    public final String name;
    public final List<String> command;
    public String description = "";
    public String timeout = "";
    public boolean mutating;
    public boolean approve;
    public final List<Param> params = new ArrayList<>();

    public Action(String name, String... command) {
        this.name = name;
        this.command = List.of(command);
    }

    public Action timeout(String t) { timeout = t; return this; }
    public Action mutating() { mutating = true; return this; }
    public Action approve() { approve = true; return this; }
    public Action param(Param p) { params.add(p); return this; }

    /** A parameter: a scalar of type (string, integer, number, boolean); dflt null — none. */
    public record Param(String name, String type, boolean required, String description, Object dflt, List<Object> enumValues) {
        public static Param of(String name, String type, boolean required) {
            return new Param(name, type, required, "", null, List.of());
        }

        public Param withDefault(Object d) { return new Param(name, type, required, description, d, enumValues); }
        public Param withEnum(Object... e) { return new Param(name, type, required, description, dflt, List.of(e)); }

        Map<String, Object> toJson() {
            Map<String, Object> out = new LinkedHashMap<>();
            out.put("type", type);
            if (!description.isEmpty()) out.put("description", description);
            if (dflt != null) out.put("default", dflt);
            if (!enumValues.isEmpty()) out.put("enum", new ArrayList<>(enumValues));
            return out;
        }
    }

    void check() {
        if (!NAME.matcher(name).matches()) throw new SchemaException("x-pack-actions: \"" + name + "\": a name of lower case letters, digits and dashes");
        if (command.isEmpty()) throw new SchemaException("x-pack-actions " + name + ": no command");
        java.util.Set<String> seen = new java.util.HashSet<>();
        for (Param p : params) {
            if (!PARAM.matcher(p.name()).matches()) throw new SchemaException("x-pack-actions " + name + ": parameter \"" + p.name() + "\": a name of lower case letters, digits and underscores");
            if (!seen.add(p.name())) throw new SchemaException("x-pack-actions " + name + ": parameter \"" + p.name() + "\" twice");
            if (!TYPES.contains(p.type())) throw new SchemaException("x-pack-actions " + name + ": parameter " + p.name() + ": type is one of " + TYPES);
        }
    }

    Map<String, Object> toJson() {
        Map<String, Object> out = new LinkedHashMap<>();
        out.put("name", name);
        if (!description.isEmpty()) out.put("description", description);
        out.put("command", new ArrayList<>(command));
        if (!timeout.isEmpty()) out.put("timeout", timeout);
        if (mutating) out.put("mutating", true);
        if (approve) out.put("approve", true);
        if (!params.isEmpty()) {
            List<Param> sorted = new ArrayList<>(params);
            sorted.sort(Comparator.comparing(Param::name));
            Map<String, Object> props = new LinkedHashMap<>();
            List<Object> required = new ArrayList<>();
            for (Param p : sorted) {
                props.put(p.name(), p.toJson());
                if (p.required()) required.add(p.name());
            }
            Map<String, Object> ps = new LinkedHashMap<>();
            ps.put("type", "object");
            ps.put("properties", props);
            if (!required.isEmpty()) ps.put("required", required);
            ps.put("additionalProperties", false);
            out.put("params", ps);
        }
        return out;
    }
}
