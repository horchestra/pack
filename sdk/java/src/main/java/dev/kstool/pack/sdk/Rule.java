package dev.kstool.pack.sdk;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/** x-pack-rules on a node: a CEL expression that must hold of the values. */
public final class Rule {
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

    public Rule phase(String phase) {
        phases.add(phase);
        return this;
    }

    Map<String, Object> toJson() {
        Map<String, Object> out = new LinkedHashMap<>();
        out.put("rule", rule);
        if (!message.isEmpty()) out.put("message", message);
        if (!phases.isEmpty()) out.put("phases", new ArrayList<>(phases));
        return out;
    }
}
