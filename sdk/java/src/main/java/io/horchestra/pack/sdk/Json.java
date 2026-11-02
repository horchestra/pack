package io.horchestra.pack.sdk;

import java.util.Collection;
import java.util.Map;

/** The JSON a schema needs, written the way Go's encoding/json writes it: compact, <, > and & escaped. */
final class Json {
    private Json() {
    }

    static String write(Object v) {
        StringBuilder sb = new StringBuilder();
        write(sb, v);
        return sb.toString();
    }

    private static void write(StringBuilder sb, Object v) {
        switch (v) {
            case null -> sb.append("null");
            case String s -> string(sb, s);
            case Boolean b -> sb.append(b);
            case Integer i -> sb.append(i);
            case Long l -> sb.append(l);
            case Short s -> sb.append(s);
            case Byte b -> sb.append(b);
            case Number n -> sb.append(number(n.doubleValue()));
            case Map<?, ?> m -> {
                sb.append('{');
                boolean first = true;
                for (Map.Entry<?, ?> e : m.entrySet()) {
                    if (!first) sb.append(',');
                    first = false;
                    string(sb, String.valueOf(e.getKey()));
                    sb.append(':');
                    write(sb, e.getValue());
                }
                sb.append('}');
            }
            case Collection<?> c -> {
                sb.append('[');
                boolean first = true;
                for (Object e : c) {
                    if (!first) sb.append(',');
                    first = false;
                    write(sb, e);
                }
                sb.append(']');
            }
            default -> throw new SchemaException("cannot write " + v.getClass().getName() + " as JSON");
        }
    }

    private static String number(double d) {
        if (d == Math.rint(d) && Math.abs(d) < 1e15) return Long.toString((long) d);
        return Double.toString(d);
    }

    private static void string(StringBuilder sb, String s) {
        sb.append('"');
        for (int i = 0; i < s.length(); i++) {
            char c = s.charAt(i);
            switch (c) {
                case '"' -> sb.append("\\\"");
                case '\\' -> sb.append("\\\\");
                case '\n' -> sb.append("\\n");
                case '\r' -> sb.append("\\r");
                case '\t' -> sb.append("\\t");
                case '<', '>', '&', ' ', ' ' -> sb.append(String.format("\\u%04x", (int) c));
                default -> {
                    if (c < 0x20) sb.append(String.format("\\u%04x", (int) c));
                    else sb.append(c);
                }
            }
        }
        sb.append('"');
    }
}
