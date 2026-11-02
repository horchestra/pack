package dev.kstool.pack.sdk;

import java.lang.reflect.ParameterizedType;
import java.lang.reflect.Type;
import java.util.ArrayList;
import java.util.List;
import java.util.regex.Pattern;

/**
 * The names of a pack's fields in formulas and rules, found from the fields themselves: a name that is not a field is
 * refused, and a renamed json name moves with the field.
 *
 * <pre>
 * Paths.pointer(OpenBao.class, "bao", "storage")   -> "/openbao/storage"   the key of packCompute / packRules
 * Paths.dotted(OpenBao.class, "postgres")          -> "postgres"            as a formula reads it
 * </pre>
 *
 * The names are those of the Java fields; the result is those of the schema.
 */
public final class Paths {
    private static final Pattern IDENT = Pattern.compile("^[A-Za-z_][A-Za-z0-9_]*$");

    private Paths() {
    }

    private static List<String> names(Class<?> root, String[] fields) {
        if (fields.length == 0) throw new SchemaException("name a field of the pack");
        List<String> out = new ArrayList<>();
        Type cur = root;
        for (String f : fields) {
            if (cur instanceof ParameterizedType p) cur = p.getRawType();
            if (!(cur instanceof Class<?> c) || !(Reflect.isStruct(c) || Pack.class.isAssignableFrom(c))) {
                throw new SchemaException(String.join(".", out) + " has no fields: " + f);
            }
            Reflect.Prop found = null;
            for (Reflect.Prop p : Reflect.props(c)) {
                if (p.field().getName().equals(f)) found = p;
            }
            if (found == null) throw new SchemaException(c.getSimpleName() + " has no field " + f + " in the schema");
            out.add(found.name());
            cur = found.field().getGenericType();
        }
        return out;
    }

    /** The JSON Pointer of a field of root, by the Java names along the way. */
    public static String pointer(Class<? extends Pack> root, String... fields) {
        StringBuilder sb = new StringBuilder();
        for (String n : names(root, fields)) sb.append('/').append(n.replace("~", "~0").replace("/", "~1"));
        return sb.toString();
    }

    /** The path of a field the way a formula or a rule reads it; a name a formula cannot select is refused. */
    public static String dotted(Class<? extends Pack> root, String... fields) {
        List<String> names = names(root, fields);
        for (String n : names) {
            if (!IDENT.matcher(n).matches()) throw new SchemaException("\"" + n + "\" is not a name a formula can select");
        }
        return String.join(".", names);
    }
}
