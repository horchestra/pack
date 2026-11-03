package io.horchestra.pack.sdk;

import java.util.LinkedHashMap;
import java.util.Map;

/** x-pack-suggest on a node: a CEL expression pack fill offers the answer of for a setting not given, with why -
 *  never a value by itself, as a formula's. */
public final class Suggest {
    public final String expr;
    public final String why;

    public Suggest(String expr, String why) {
        this.expr = expr;
        this.why = why == null ? "" : why;
    }

    Map<String, Object> toJson() {
        Map<String, Object> out = new LinkedHashMap<>();
        out.put("expr", expr);
        if (!why.isEmpty()) out.put("why", why);
        return out;
    }
}
