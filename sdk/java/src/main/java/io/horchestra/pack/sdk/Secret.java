package io.horchestra.pack.sdk;

import java.util.LinkedHashMap;
import java.util.Map;

/** A credential leaf: the operator gives a reference to a Secret, never a value. */
public final class Secret {
    public String name = "";
    public String key = "";

    boolean isZero() {
        return name.isEmpty() && key.isEmpty();
    }

    Map<String, Object> toJson() {
        Map<String, Object> out = new LinkedHashMap<>();
        if (!isZero()) {
            Map<String, Object> ref = new LinkedHashMap<>();
            ref.put("name", name);
            ref.put("key", key);
            out.put("secretRef", ref);
        }
        return out;
    }
}
