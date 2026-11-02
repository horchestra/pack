package dev.kstool.pack.sdk;

import java.util.Map;
import java.util.TreeMap;

/** Everything about a pack that goes into its OCI artifact. name and version are required. */
public final class Meta {
    public final String name; // a DNS label; the key of the artifact among the packs of a pack
    public final String version;
    public String description = "";
    public String documentation = "";
    public String source = "";
    public String url = "";
    public String license = "";
    public String vendor = "";
    public String authors = "";
    public String created = ""; // RFC 3339; empty - not written, so that the artifact is the same byte for byte
    public String image = "";    // oci://repo:tag the application runs; "" - the pack runs nothing itself
    public String artifact = ""; // oci://repo where the pack is published: what a pack that holds it mounts it from
    public final Map<String, String> annotations = new TreeMap<>();
    public String dir = "";      // the pack's directory, whose content/ holds its files; "" - the working directory

    public Meta(String name, String version) {
        this.name = name;
        this.version = version;
    }

    public Meta description(String v) { description = v; return this; }
    public Meta documentation(String v) { documentation = v; return this; }
    public Meta source(String v) { source = v; return this; }
    public Meta url(String v) { url = v; return this; }
    public Meta license(String v) { license = v; return this; }
    public Meta vendor(String v) { vendor = v; return this; }
    public Meta authors(String v) { authors = v; return this; }
    public Meta created(String v) { created = v; return this; }
    public Meta image(String v) { image = v; return this; }
    public Meta artifact(String v) { artifact = v; return this; }
    public Meta dir(String v) { dir = v; return this; }
    public Meta annotation(String k, String v) { annotations.put(k, v); return this; }
}
