package io.horchestra.pack.sdk;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/** x-pack-rules on a node: a CEL expression that must hold of the values. The id names it among the pack's rules, so
 *  that docs/&lt;lang&gt;.yaml can say its message in other languages (rules: {&lt;id&gt;: …}). */
public final class Rule {
    public String id = "";
    public final String rule;
    public String message = "";
    public final List<String> phases = new ArrayList<>();

    public Rule(String rule) {
        this.rule = rule;
    }

    public Rule message(String message) {
        this.message = message;
        return this;
    }

    public Rule id(String id) {
        this.id = id;
        return this;
    }

    public Rule phase(String phase) {
        phases.add(phase);
        return this;
    }

    Map<String, Object> toJson() {
        Map<String, Object> out = new LinkedHashMap<>();
        if (!id.isEmpty()) out.put("id", id);
        out.put("rule", rule);
        if (!message.isEmpty()) out.put("message", message);
        if (!phases.isEmpty()) out.put("phases", new ArrayList<>(phases));
        return out;
    }
}
