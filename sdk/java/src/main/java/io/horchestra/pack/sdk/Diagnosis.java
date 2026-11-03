package io.horchestra.pack.sdk;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/** One of x-pack-diagnose on the root of a pack: what a failure looks like (match, RE2 over the step's error and
 *  logs), what it is (cause), the ways out (fix). */
public final class Diagnosis {
    public final String id;
    public final String match;
    public final String cause;
    public final List<Fix> fix = new ArrayList<>();

    public Diagnosis(String id, String match, String cause) {
        this.id = id;
        this.match = match;
        this.cause = cause;
    }

    /** A value for a setting: the setting's pointer and CEL of the value. */
    public Diagnosis patch(String pointer, String expr, String why) {
        fix.add(new Fix(pointer, expr, "", why));
        return this;
    }

    /** A command to run. */
    public Diagnosis command(String command, String why) {
        fix.add(new Fix("", "", command, why));
        return this;
    }

    /** A way out of a failure. */
    public record Fix(String patch, String expr, String command, String why) {
        Map<String, Object> toJson() {
            Map<String, Object> out = new LinkedHashMap<>();
            if (!patch.isEmpty()) out.put("patch", patch);
            if (!expr.isEmpty()) out.put("expr", expr);
            if (!command.isEmpty()) out.put("command", command);
            if (!why.isEmpty()) out.put("why", why);
            return out;
        }
    }

    Map<String, Object> toJson() {
        Map<String, Object> out = new LinkedHashMap<>();
        out.put("id", id);
        out.put("match", match);
        out.put("cause", cause);
        if (!fix.isEmpty()) {
            List<Object> fs = new ArrayList<>();
            for (Fix f : fix) fs.add(f.toJson());
            out.put("fix", fs);
        }
        return out;
    }
}
