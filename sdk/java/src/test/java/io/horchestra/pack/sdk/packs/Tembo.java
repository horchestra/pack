package io.horchestra.pack.sdk.packs;

import java.util.Map;

import io.horchestra.pack.sdk.Paths;
import io.horchestra.pack.sdk.Suggest;
import io.horchestra.pack.sdk.Meta;
import io.horchestra.pack.sdk.Pack;
import io.horchestra.pack.sdk.Root;
import io.horchestra.pack.sdk.Secret;
import io.horchestra.pack.sdk.Setting;

/** tembo, the pack of the Go tests (internal/testpacks/tembo), written in Java. */
public class Tembo extends Pack {
    /** The node's tags and the log it keeps. */
    public static class Node {
        @Setting(json = "raft_retention_log_size", omitempty = true)
        public int raftRetentionLogSize;
        @Setting(omitempty = true)
        public boolean nofailover;
        @Setting(omitempty = true)
        public boolean nosync;
        @Setting(json = "failover_priority", omitempty = true)
        public int failoverPriority;
    }

    @Setting(env = "TEMBO_ID", omitempty = true)
    public String id = "";
    @Setting(env = "TEMBO_LISTEN", omitempty = true)
    public String listen = "";
    @Setting(json = "listen_ro", env = "TEMBO_LISTEN_RO", omitempty = true)
    public String listenRo = "";
    @Setting(env = "TEMBO_DB", omitempty = true)
    public String db = "";
    @Setting(json = "pg_data", env = "TEMBO_PG_DATA", omitempty = true)
    public String pgData = "";
    @Setting(json = "pg_port", env = "TEMBO_PG_PORT", omitempty = true)
    public int pgPort;
    @Setting(json = "pg_password", env = "TEMBO_PG_PASSWORD", omitempty = true)
    public Secret pgPassword = new Secret();
    @Setting(env = "TEMBO_CONFIG", omitempty = true)
    public String config = "";
    @Setting(omitempty = true)
    public Node node = new Node();

    @Override
    public void setDefaults() {
        listen = ":5432";
        listenRo = ":5434";
        db = "/var/lib/postgresql/tembo.db";
        pgData = "/var/lib/postgresql/data";
        pgPort = 5433;
        config = "/etc/tembo/tembo.env";
        node.raftRetentionLogSize = 8 << 20;
    }

    @Override
    public Meta meta() {
        return new Meta("tembo", "0.2.0").description("PostgreSQL with raft").license("Apache-2.0")
                .source("https://example.org/tembo").image("images/tembo:0.2.0")
                .dir("src/test/packs/tembo");
    }

    /** The port next to the agent's own when a host of the target already listens on it. */
    @Override
    public Map<String, Suggest> packSuggest() {
        return Map.of(Paths.pointer(Tembo.class, "pgPort"), new Suggest(
                "has(facts.hosts) && facts.hosts.exists(h, has(facts.hosts[h].ports) && facts.hosts[h].ports.exists(p, p.port == 5433)) ? 5434 : null", "5433 is taken on a host"));
    }

    @Override
    public Root packRoot() {
        return new Root()
                .port("api", 8001).port("pg", 5432).port("pg-ro", 5434)
                .content(new Root.File("/etc/tembo/tembo.env").template("tembo.env.ptmpl").values("node").parseAs("text").mode("0640"))
                .data("data", new Root.Volume("/var/lib/postgresql").size("10Gi"))
                .minimum("memory", "1Gi")
                .command("tembo", "run")
                .readiness(Root.Readiness.http("api/health"));
    }
}
