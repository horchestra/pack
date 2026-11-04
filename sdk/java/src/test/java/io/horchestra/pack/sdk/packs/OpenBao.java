package io.horchestra.pack.sdk.packs;

import io.horchestra.pack.sdk.Meta;
import io.horchestra.pack.sdk.Pack;
import io.horchestra.pack.sdk.Paths;
import io.horchestra.pack.sdk.Root;
import io.horchestra.pack.sdk.Rule;
import io.horchestra.pack.sdk.Setting;

import java.util.ArrayList;
import java.util.List;
import java.util.Map;

/** openbao, which holds tembo, the pack of the Go tests (internal/testpacks/openbao), written in Java. */
public class OpenBao extends Pack {
    public static class StorageBlock {
        public String type = "";
        @Setting(omitempty = true)
        public Map<String, Object> config = new java.util.TreeMap<>();
    }

    public static class Config {
        @Setting(omitempty = true)
        public boolean ui;
        @Setting(json = "log_level", omitempty = true, enumOf = {"trace", "debug", "info", "warn", "error"})
        public String logLevel = "";
        @Setting(json = "api_addr")
        public String apiAddr = "";
        @Setting(omitempty = true)
        public List<StorageBlock> storage = new ArrayList<>();
    }

    @Setting(json = "openbao")
    public Config bao = new Config();
    @Setting(omitempty = true)
    public Tembo postgres;

    public static String version = "2.1.0";
    public static String image = "images/openbao:2.1.0";

    @Override
    public void setDefaults() {
        bao.logLevel = "info";
    }

    // The storage of the embedded postgres is derived, not asked: the formula fills it when the operator named
    // postgres and wrote no storage himself.
    @Override
    public Map<String, String> packCompute() {
        String pg = Paths.dotted(OpenBao.class, "postgres");
        return Map.of(Paths.pointer(OpenBao.class, "bao", "storage"),
                "has(pack.values." + pg + ") ? [{'type': 'postgresql', 'config': {'connection_url': 'postgres://' + pack.keys."
                        + pg + ".service + ':5432/openbao'}}] : null");
    }

    // ... and the other half of the fact: with the embedded postgres chosen, storage is the formula's.
    @Override
    public Map<String, List<Rule>> packRules() {
        String pg = Paths.dotted(OpenBao.class, "postgres"), storage = Paths.dotted(OpenBao.class, "bao", "storage");
        return Map.of(Paths.pointer(OpenBao.class, "bao", "storage"), List.of(
                new Rule("!has(pack.values." + pg + ") || !has(values." + storage + ") || size(values." + storage + ") == 0")
                        .message("storage is filled from the embedded postgres and must not be set").id("storage-from-postgres")));
    }

    @Override
    public Meta meta() {
        return new Meta("openbao", version).description("secrets").license("MPL-2.0").image(image).annotation("example.org/tier", "core")
                .dir("src/test/packs/openbao");
    }

    @Override
    public Root packRoot() {
        return new Root().content(new Root.File("/etc/openbao/config.json").values("openbao")).port("api", 8200).readiness(Root.Readiness.http("api/v1/sys/health"))
                .savepoint(Paths.dotted(OpenBao.class, "postgres"));
    }
}
