package dev.kstool.pack.sdk;

import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;
import java.util.regex.Pattern;

/** The little YAML pack.yaml needs: maps (keys sorted), lists, strings. */
final class Yaml {
    private static final Pattern PLAIN = Pattern.compile("^[A-Za-z0-9_./@+][A-Za-z0-9_./:@+ -]*$");
    private static final Pattern RESERVED = Pattern.compile("^(true|false|null|yes|no|on|off|y|n|~)$", Pattern.CASE_INSENSITIVE);
    private static final Pattern NUMBER = Pattern.compile("^[-+]?(\\d[\\d_]*)?\\.?\\d*([eE][-+]?\\d+)?$|^0[xob][0-9a-fA-F_]+$");

    private Yaml() {
    }

    static String dump(Object v) {
        StringBuilder sb = new StringBuilder();
        lines(sb, v, 0);
        return sb.toString();
    }

    private static String scalar(Object o) {
        String s = String.valueOf(o);
        boolean plain = PLAIN.matcher(s).find() && !s.endsWith(":") && !s.endsWith(" ") && !s.contains(": ")
                && !RESERVED.matcher(s).find() && !NUMBER.matcher(s).find();
        return plain ? s : Json.write(s).replace("\\u003c", "<").replace("\\u003e", ">").replace("\\u0026", "&");
    }

    @SuppressWarnings("unchecked")
    private static void lines(StringBuilder sb, Object v, int indent) {
        String pad = "  ".repeat(indent);
        if (v instanceof Map<?, ?> m) {
            for (Map.Entry<String, Object> e : new TreeMap<>((Map<String, Object>) m).entrySet()) {
                Object x = e.getValue();
                if (x instanceof Map || x instanceof List) {
                    sb.append(pad).append(scalar(e.getKey())).append(":\n");
                    lines(sb, x, indent + (x instanceof List ? 0 : 1));
                } else {
                    sb.append(pad).append(scalar(e.getKey())).append(": ").append(scalar(x)).append('\n');
                }
            }
        } else if (v instanceof List<?> l) {
            for (Object e : l) {
                if (e instanceof Map) {
                    StringBuilder item = new StringBuilder();
                    lines(item, e, indent + 1);
                    // the first line of the map carries the dash
                    sb.append(pad).append("- ").append(item.substring(pad.length() + 2));
                } else {
                    sb.append(pad).append("- ").append(scalar(e)).append('\n');
                }
            }
        }
    }

    static List<Object> list(Object... items) {
        return new ArrayList<>(List.of(items));
    }
}
