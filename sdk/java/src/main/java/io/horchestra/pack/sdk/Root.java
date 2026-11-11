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
    public final List<File> content = new ArrayList<>();
    public final Map<String, OSInstall> packages = new TreeMap<>(); // by OS: the key is the target's to match
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
    public Root content(File f) { content.add(f); return this; }
    public Root packages(String os, OSInstall p) { packages.put(os, p); return this; }
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
        if (!content.isEmpty()) {
            List<Object> fs = new ArrayList<>();
            for (File f : content) fs.add(f.toJson());
            out.put("content", fs);
        }
        if (!packages.isEmpty()) {
            Map<String, Object> ps = new TreeMap<>();
            packages.forEach((k, v) -> ps.put(k, v.toJson()));
            out.put("packages", ps);
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

    /** A file in the component's containers: from source (a file of content/, byte for byte), template (a file of
     *  content/ rendered over the values the pack sees; with values, only when that value is there), or values (a
     *  field's subtree, "conf", written in format, the name of the path saying it when there is none). */
    public static final class File {
        public final String path;
        public String source = "", template = "", url = "", values = "", format = "", parseAs = "", mode = "";
        public boolean perInstance;

        public File(String path) { this.path = path; }

        public File source(String s) { source = s; return this; }
        public File template(String t) { template = t; return this; }
        public File url(String u) { url = u; return this; }
        public File values(String v) { values = v; return this; }
        public File format(String f) { format = f; return this; }
        public File parseAs(String p) { parseAs = p; return this; }
        public File mode(String m) { mode = m; return this; }
        public File perInstance() { perInstance = true; return this; }

        Map<String, Object> toJson() {
            Map<String, Object> out = new LinkedHashMap<>();
            out.put("path", path);
            if (!source.isEmpty()) out.put("source", source);
            if (!template.isEmpty()) out.put("template", template);
            if (!url.isEmpty()) out.put("url", url);
            if (!values.isEmpty()) out.put("values", values);
            if (!format.isEmpty()) out.put("format", format);
            if (!parseAs.isEmpty()) out.put("parseAs", parseAs);
            if (!mode.isEmpty()) out.put("mode", mode);
            if (perInstance) out.put("perInstance", true);
            return out;
        }
    }

    /** The installation on one OS, in this order: prepare installed, sysctls set and modules loaded, repo
     *  delivered, names installed, commands run, content delivered, services started. A name or the url of a file
     *  is a template too; the target resolves ${os:codename}, ${os:version}, ${os:arch}, ${os:id} and ${os:kernel}. */
    public static final class OSInstall {
        public final List<String> prepare = new ArrayList<>(), names = new ArrayList<>(), services = new ArrayList<>();
        public final List<String> modules = new ArrayList<>();
        public final Map<String, String> sysctls = new TreeMap<>();
        public final List<File> repo = new ArrayList<>(), content = new ArrayList<>();
        public final List<List<String>> commands = new ArrayList<>();

        public OSInstall names(String... v) { names.addAll(List.of(v)); return this; }
        public OSInstall prepare(String... v) { prepare.addAll(List.of(v)); return this; }
        public OSInstall sysctl(String name, String value) { sysctls.put(name, value); return this; }
        public OSInstall module(String name) { modules.add(name); return this; }
        public OSInstall repo(File f) { repo.add(f); return this; }
        public OSInstall command(String... argv) { commands.add(List.of(argv)); return this; }
        public OSInstall content(File f) { content.add(f); return this; }
        public OSInstall services(String... v) { services.addAll(List.of(v)); return this; }

        private static List<Object> json(List<File> fs) {
            List<Object> out = new ArrayList<>();
            for (File f : fs) out.add(f.toJson());
            return out;
        }

        Map<String, Object> toJson() {
            Map<String, Object> out = new LinkedHashMap<>();
            if (!prepare.isEmpty()) out.put("prepare", new ArrayList<>(prepare));
            if (!sysctls.isEmpty()) out.put("sysctls", new TreeMap<>(sysctls));
            if (!modules.isEmpty()) out.put("modules", new ArrayList<>(modules));
            if (!repo.isEmpty()) out.put("repo", json(repo));
            out.put("names", new ArrayList<>(names));
            if (!commands.isEmpty()) out.put("commands", new ArrayList<>(commands));
            if (!content.isEmpty()) out.put("content", json(content));
            if (!services.isEmpty()) out.put("services", new ArrayList<>(services));
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
