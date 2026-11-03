package io.horchestra.pack.sdk;

import java.lang.reflect.Field;
import java.lang.reflect.Modifier;
import java.lang.reflect.ParameterizedType;
import java.lang.reflect.Type;
import java.util.ArrayList;
import java.util.Collection;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.TreeMap;

/**
 * Reflect: a pack class into its JSON Schema, the way the Go sdk does it. The fields name the properties and a field
 * without omitempty is required; the value after setDefaults is the defaults document. A field whose type is a pack
 * becomes a reference to its schema.
 */
final class Reflect {
    static final String SCHEMA = "https://json-schema.org/draft/2020-12/schema";
    static final String PACK_SCHEME = "pack://";

    private Reflect() {
    }

    // ---- the fields

    /** A setting: a public non-static field that is in the schema. */
    record Prop(Field field, String name, Setting tags) {
        boolean omitempty() {
            return tags != null && tags.omitempty();
        }
    }

    static List<Prop> props(Class<?> c) {
        List<Prop> out = new ArrayList<>();
        for (Field f : c.getDeclaredFields()) {
            int m = f.getModifiers();
            if (Modifier.isStatic(m) || Modifier.isTransient(m) || f.isSynthetic()) continue;
            Setting s = f.getAnnotation(Setting.class);
            if (s != null && (s.skip() || "-".equals(s.json()))) continue;
            String name = s != null && !s.json().isEmpty() ? s.json() : f.getName();
            f.setAccessible(true);
            out.add(new Prop(f, name, s));
        }
        return out;
    }

    static Class<?> raw(Type t) {
        if (t instanceof Class<?> c) return c;
        if (t instanceof ParameterizedType p) return (Class<?>) p.getRawType();
        throw new SchemaException("no JSON Schema for the type " + t);
    }

    static boolean isPack(Type t) {
        return t instanceof Class<?> c && Pack.class.isAssignableFrom(c);
    }

    static boolean isStruct(Type t) {
        if (!(t instanceof Class<?> c)) return false;
        return !c.isPrimitive() && !c.isEnum() && !c.isArray() && c != Secret.class && c != Object.class
                && !c.getName().startsWith("java.");
    }

    static Object get(Field f, Object o) {
        try {
            return f.get(o);
        } catch (IllegalAccessException e) {
            throw new SchemaException(f + ": " + e.getMessage());
        }
    }

    // ---- values

    static boolean isZero(Object v) {
        return switch (v) {
            case null -> true;
            case String s -> s.isEmpty();
            case Boolean b -> !b;
            case Number n -> n.doubleValue() == 0;
            case Collection<?> c -> c.isEmpty();
            case Map<?, ?> m -> m.isEmpty();
            case Secret s -> s.isZero();
            default -> {
                for (Prop p : props(v.getClass())) {
                    if (!isZero(get(p.field(), v))) yield false;
                }
                yield true;
            }
        };
    }

    /** encoding/json of a value: every field but the optional zero ones. */
    static Object toJson(Object v) {
        if (v instanceof Secret s) return s.toJson();
        if (v instanceof Collection<?> c) {
            List<Object> out = new ArrayList<>();
            for (Object e : c) out.add(toJson(e));
            return out;
        }
        if (v instanceof Map<?, ?> m) {
            Map<String, Object> out = new TreeMap<>();
            m.forEach((k, e) -> out.put(String.valueOf(k), toJson(e)));
            return out;
        }
        if (v != null && isStruct(v.getClass())) {
            Map<String, Object> out = new LinkedHashMap<>();
            for (Prop p : props(v.getClass())) {
                Object fv = get(p.field(), v);
                if (p.omitempty() && isZero(fv)) continue;
                out.put(p.name(), toJson(fv));
            }
            return out;
        }
        return v;
    }

    /** Drop zero values - "", 0, false, null, empty objects and arrays - from a marshalled value. */
    static Object prune(Object v) {
        Object[] kept = prune1(toJson(v));
        return kept[0];
    }

    @SuppressWarnings("unchecked")
    private static Object[] prune1(Object v) {
        if (v instanceof Map<?, ?> m) {
            Map<String, Object> out = new TreeMap<>();
            for (Map.Entry<String, Object> e : ((Map<String, Object>) m).entrySet()) {
                Object[] k = prune1(e.getValue());
                if ((Boolean) k[1]) out.put(e.getKey(), k[0]);
            }
            return new Object[]{out, !out.isEmpty()};
        }
        if (v instanceof List<?> l) return new Object[]{l, !l.isEmpty()};
        if (v instanceof String s) return new Object[]{s, !s.isEmpty()};
        if (v instanceof Boolean b) return new Object[]{b, b};
        if (v instanceof Number n) return new Object[]{n, n.doubleValue() != 0};
        if (v == null) return new Object[]{null, false};
        return new Object[]{v, true};
    }

    /** A field set to something is a setting with that default; a struct is not one itself, nor is a secret. */
    static boolean leafWithDefault(Object v) {
        return v != null && !(v instanceof Secret) && !isStruct(v.getClass()) && !isZero(v);
    }

    // ---- types

    static Map<String, Object> typeSchema(Type t) {
        Map<String, Object> out = new LinkedHashMap<>();
        Class<?> c = raw(t);
        if (c == Secret.class) {
            out.put("type", "string");
            out.put("format", "password");
        } else if (c == boolean.class || c == Boolean.class) {
            out.put("type", "boolean");
        } else if (c == int.class || c == Integer.class || c == long.class || c == Long.class || c == short.class
                || c == Short.class || c == byte.class || c == Byte.class) {
            out.put("type", "integer");
        } else if (c == double.class || c == Double.class || c == float.class || c == Float.class) {
            out.put("type", "number");
        } else if (c == String.class) {
            out.put("type", "string");
        } else if (c == Object.class) {
            // any: no type
        } else if (Collection.class.isAssignableFrom(c)) {
            out.put("items", t instanceof ParameterizedType p ? typeSchema(p.getActualTypeArguments()[0]) : new LinkedHashMap<>());
            out.put("type", "array");
        } else if (Map.class.isAssignableFrom(c)) {
            Type value = t instanceof ParameterizedType p ? p.getActualTypeArguments()[1] : Object.class;
            if (value == Object.class) {
                out.put("type", "object");
            } else {
                // a typed map is the Go schema's map: its keys marshal sorted
                out.put("additionalProperties", typeSchema(value));
                out.put("type", "object");
            }
        } else if (isStruct(c)) {
            Map<String, Object> props = new LinkedHashMap<>();
            List<Object> required = new ArrayList<>();
            for (Prop p : props(c)) {
                props.put(p.name(), typeSchema(p.field().getGenericType()));
                if (!p.omitempty()) required.add(p.name());
            }
            out.put("properties", props);
            out.put("additionalProperties", false);
            out.put("type", "object");
            if (!required.isEmpty()) out.put("required", required);
        } else {
            throw new SchemaException("no JSON Schema for the type " + t);
        }
        return out;
    }

    // ---- the schema

    static Map<String, Object> schema(Pack p) {
        p.setDefaults();
        Map<String, Object> s = new LinkedHashMap<>();
        s.put("$schema", SCHEMA);
        s.putAll(typeSchema(p.getClass()));
        annotate(s, p.getClass(), p, "");
        s.put("x-pack", p.packRoot().toJson());
        for (Map.Entry<String, String> e : new TreeMap<>(p.packCompute()).entrySet()) {
            Map<String, Object> node = lookup(s, e.getKey(), "x-pack-compute");
            node.put("x-pack-compute", e.getValue());
            sortExtensions(node);
        }
        for (Map.Entry<String, Suggest> e : new TreeMap<>(p.packSuggest()).entrySet()) {
            if (e.getValue().expr == null || e.getValue().expr.isEmpty()) throw new SchemaException("x-pack-suggest " + e.getKey() + ": no expression");
            Map<String, Object> node = lookup(s, e.getKey(), "x-pack-suggest");
            node.put("x-pack-suggest", e.getValue().toJson());
            sortExtensions(node);
        }
        Map<String, String> ids = new TreeMap<>();
        for (Map.Entry<String, List<Rule>> e : new TreeMap<>(p.packRules()).entrySet()) {
            List<Object> rules = new ArrayList<>();
            for (Rule r : e.getValue()) {
                if (!r.id.isEmpty() && ids.containsKey(r.id)) {
                    throw new SchemaException("x-pack-rules: the id \"" + r.id + "\" is of a rule on " + ids.get(r.id) + " and on " + e.getKey());
                }
                if (!r.id.isEmpty()) ids.put(r.id, e.getKey());
                rules.add(r.toJson());
            }
            Map<String, Object> node = lookup(s, e.getKey(), "x-pack-rules");
            node.put("x-pack-rules", rules);
            sortExtensions(node);
        }
        List<Docs.File> docs = Docs.load(java.nio.file.Path.of(p.meta().dir.isEmpty() ? "." : p.meta().dir));
        if (docs != null) Docs.apply(s, docs);
        return s;
    }

    /** The x- keys of a node come after the others and in order, as the Go sdk writes them: a leaf may hold several. */
    private static void sortExtensions(Map<String, Object> node) {
        for (String k : new TreeMap<>(node).keySet()) {
            if (k.startsWith("x-")) node.put(k, node.remove(k));
        }
    }

    /** Defaults, files and environment variables onto the node of a struct. A struct that is not set (null) has
     *  none of them, as in the Go sdk. */
    @SuppressWarnings("unchecked")
    private static void annotate(Map<String, Object> node, Class<?> c, Object value, String path) {
        if (value == null) return;
        Map<String, Object> properties = (Map<String, Object>) node.get("properties");
        for (Prop p : props(c)) {
            Type t = p.field().getGenericType();
            Object fv = get(p.field(), value);
            if (isPack(t)) {
                if (!path.isEmpty()) {
                    throw new SchemaException(path + "/" + p.name() + ": a pack is held by a field of the pack itself, not of a struct inside it");
                }
                Pack held = instance((Class<? extends Pack>) t);
                Map<String, Object> ref = new LinkedHashMap<>();
                ref.put("$ref", PACK_SCHEME + held.meta().name + "#");
                if (fv != null && !isZero(fv)) ref.put("default", prune(fv));
                properties.put(p.name(), ref);
                continue;
            }
            Map<String, Object> prop = (Map<String, Object>) properties.get(p.name());
            if (leafWithDefault(fv)) prop.put("default", toJson(fv));
            if (p.tags() != null && p.tags().readOnly()) prop.put("readOnly", true);
            if (p.tags() != null && !p.tags().env().isEmpty() && !p.tags().env().equals("-")) {
                prop.put("x-pack-env", p.tags().env());
            }
            if (isStruct(t)) annotate(prop, (Class<?>) t, fv, path + "/" + p.name());
        }
    }

    @SuppressWarnings("unchecked")
    private static Map<String, Object> lookup(Map<String, Object> s, String ptr, String what) {
        Map<String, Object> node = s;
        String rest = ptr.startsWith("/") ? ptr.substring(1) : ptr;
        for (String seg : rest.split("/", -1)) {
            Object props = node.get("properties");
            if (props == null) throw new SchemaException(what + " " + ptr + ": \"" + seg + "\": not an object");
            Object next = ((Map<String, Object>) props).get(seg);
            if (next == null) throw new SchemaException(what + " " + ptr + ": \"" + seg + "\": no such property");
            node = (Map<String, Object>) next;
        }
        return node;
    }

    static <T extends Pack> T instance(Class<T> c) {
        try {
            var ctor = c.getDeclaredConstructor();
            ctor.setAccessible(true);
            return ctor.newInstance();
        } catch (ReflectiveOperationException e) {
            throw new SchemaException(c.getName() + " needs a no-argument constructor: " + e);
        }
    }
}
