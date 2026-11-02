package io.horchestra.pack.sdk;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;

/** x-pack on the root of a pack's schema: what the component is to the system around it. */
public final class Root {
    public final Map<String, Integer> ports = new TreeMap<>();
    public final Map<String, Volume> data = new TreeMap<>();
    public final Map<String, String> minimum = new TreeMap<>();
    public Readiness readiness;
    public Upgrade upgrade;
    public final List<Hook> hooks = new ArrayList<>();
    public final List<String> command = new ArrayList<>(); // replaces the image's entrypoint+cmd wholesale
    public String restartPolicy = "";                      // Always | OnFailure | Never
    public final List<String> savepoints = new ArrayList<>();
    public Rollout rollout;

    public Root port(String name, int port) { ports.put(name, port); return this; }
    public Root data(String name, Volume v) { data.put(name, v); return this; }
    public Root minimum(String resource, String quantity) { minimum.put(resource, quantity); return this; }
    public Root readiness(Readiness r) { readiness = r; return this; }
    public Root upgrade(Upgrade u) { upgrade = u; return this; }
    public Root hook(Hook h) { hooks.add(h); return this; }
    public Root command(String... argv) { command.addAll(List.of(argv)); return this; }
    public Root restartPolicy(String p) { restartPolicy = p; return this; }
    public Root savepoint(String key) { savepoints.add(key); return this; }
    public Root rollout(Rollout r) { rollout = r; return this; }

    Map<String, Object> toJson() {
        Map<String, Object> out = new LinkedHashMap<>();
        if (!ports.isEmpty()) out.put("ports", new TreeMap<>(ports));
        if (!data.isEmpty()) {
            Map<String, Object> d = new TreeMap<>();
            data.forEach((k, v) -> d.put(k, v.toJson()));
            out.put("data", d);
        }
        if (!minimum.isEmpty()) out.put("minimum", new TreeMap<>(minimum));
        if (readiness != null) out.put("readiness", readiness.toJson());
        if (upgrade != null) out.put("upgrade", upgrade.toJson());
        if (!hooks.isEmpty()) {
            List<Object> hs = new ArrayList<>();
            for (Hook h : hooks) hs.add(h.toJson());
            out.put("hooks", hs);
        }
        if (!command.isEmpty()) out.put("command", new ArrayList<>(command));
        if (!restartPolicy.isEmpty()) out.put("restartPolicy", restartPolicy);
        if (!savepoints.isEmpty()) out.put("savepoints", new ArrayList<>(savepoints));
        if (rollout != null) out.put("rollout", rollout.toJson());
        return out;
    }

    /** A volume the component keeps: where it is mounted is the component's fact, how big is the operator's. */
    public static final class Volume {
        public final String mountPath;
        public String size = "";

        public Volume(String mountPath) { this.mountPath = mountPath; }

        public Volume size(String size) { this.size = size; return this; }

        Map<String, Object> toJson() {
            Map<String, Object> out = new LinkedHashMap<>();
            out.put("mountPath", mountPath);
            if (!size.isEmpty()) out.put("size", size);
            return out;
        }
    }

    public static final class Readiness {
        public String tcp = "";  // a port name
        public String http = ""; // "<port>/<path>"
        public final List<String> exec = new ArrayList<>();

        public static Readiness tcp(String port) { Readiness r = new Readiness(); r.tcp = port; return r; }
        public static Readiness http(String portAndPath) { Readiness r = new Readiness(); r.http = portAndPath; return r; }
        public static Readiness exec(String... argv) { Readiness r = new Readiness(); r.exec.addAll(List.of(argv)); return r; }

        Map<String, Object> toJson() {
            Map<String, Object> out = new LinkedHashMap<>();
            if (!tcp.isEmpty()) out.put("tcp", tcp);
            if (!http.isEmpty()) out.put("http", http);
            if (!exec.isEmpty()) out.put("exec", new ArrayList<>(exec));
            return out;
        }
    }

    public static final class Upgrade {
        public final String from; // version constraint on the version being upgraded from

        public Upgrade(String from) { this.from = from; }

        Map<String, Object> toJson() {
            Map<String, Object> out = new LinkedHashMap<>();
            out.put("from", from);
            return out;
        }
    }

    public static final class When {
        public String from = "";
        public String to = "";

        public When from(String v) { from = v; return this; }
        public When to(String v) { to = v; return this; }

        Map<String, Object> toJson() {
            Map<String, Object> out = new LinkedHashMap<>();
            if (!from.isEmpty()) out.put("from", from);
            if (!to.isEmpty()) out.put("to", to);
            return out;
        }
    }

    public static final class Step {
        public String image = ""; // new | old
        public final List<String> command = new ArrayList<>();

        public Step(String... command) { this.command.addAll(List.of(command)); }

        public Step image(String v) { image = v; return this; }

        Map<String, Object> toJson() {
            Map<String, Object> out = new LinkedHashMap<>();
            if (!image.isEmpty()) out.put("image", image);
            out.put("command", new ArrayList<>(command));
            return out;
        }
    }

    /** One step the component contributes to the saga of its own installation or upgrade. */
    public static final class Hook {
        public final String name;
        public final String phase; // preInstall | postInstall | preUpgrade | postUpgrade | preDowngrade | postDowngrade
        public final List<String> command = new ArrayList<>();
        public When when;
        public String image = "";
        public boolean exclusive;
        public String timeout = "";
        public boolean idempotent;
        public Step compensate;

        public Hook(String name, String phase, String... command) {
            this.name = name;
            this.phase = phase;
            this.command.addAll(List.of(command));
        }

        public Hook when(When w) { when = w; return this; }
        public Hook image(String v) { image = v; return this; }
        public Hook exclusive() { exclusive = true; return this; }
        public Hook timeout(String v) { timeout = v; return this; }
        public Hook idempotent() { idempotent = true; return this; }
        public Hook compensate(Step s) { compensate = s; return this; }

        Map<String, Object> toJson() {
            Map<String, Object> out = new LinkedHashMap<>();
            out.put("name", name);
            out.put("phase", phase);
            if (when != null) out.put("when", when.toJson());
            if (!image.isEmpty()) out.put("image", image);
            out.put("command", new ArrayList<>(command));
            if (exclusive) out.put("exclusive", true);
            if (!timeout.isEmpty()) out.put("timeout", timeout);
            if (idempotent) out.put("idempotent", true);
            if (compensate != null) out.put("compensate", compensate.toJson());
            return out;
        }
    }

    public static final class Rollout {
        public int maxUnavailable;

        public Rollout(int maxUnavailable) { this.maxUnavailable = maxUnavailable; }

        Map<String, Object> toJson() {
            Map<String, Object> out = new LinkedHashMap<>();
            if (maxUnavailable != 0) out.put("maxUnavailable", maxUnavailable);
            return out;
        }
    }
}
