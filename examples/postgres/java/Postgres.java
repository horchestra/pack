import io.horchestra.pack.sdk.Action;
import io.horchestra.pack.sdk.Meta;
import io.horchestra.pack.sdk.Pack;
import io.horchestra.pack.sdk.PackSdk;
import io.horchestra.pack.sdk.Paths;
import io.horchestra.pack.sdk.Root;
import io.horchestra.pack.sdk.Secret;
import io.horchestra.pack.sdk.Setting;

import java.util.List;
import java.util.Map;

/**
 * postgres: a single-instance PostgreSQL with an install hook (the extensions every database gets, after the first
 * start) and a major-version upgrade hook (the saga runs pg_upgrade with the component stopped, and takes it back if
 * it fails). The same pack is written in Go and Python next to this one; all three generate the same artifacts
 * (examples/postgres/golden).
 *
 * <pre>
 * java -cp out Postgres generate -o out/pack     (from examples/postgres/java; see ../../../sdk/java/README.md)
 * </pre>
 */
public class Postgres extends Pack {
    public static class Superuser {
        @Setting(env = "POSTGRES_USER", omitempty = true)
        public String name = "";
        @Setting(env = "POSTGRES_PASSWORD") // required: the operator names a Secret
        public Secret password = new Secret();
    }

    public static class Conf {
        @Setting(json = "max_connections", omitempty = true)
        public int maxConnections;
        @Setting(json = "shared_buffers", omitempty = true) // a quarter of the memory limit, unless the operator says
        public String sharedBuffers = "";
        @Setting(json = "listen_addresses", omitempty = true)
        public String listenAddresses = "";
        @Setting(omitempty = true)
        public int port;
    }

    @Setting(env = "POSTGRES_DB")
    public String database = ""; // required: no omitempty
    public Superuser superuser = new Superuser();
    /** postgresql.conf. */
    public Conf conf = new Conf();
    /** Where the packages of PGDG come from on a host: upstream by default, a mirror (oci-proxy of the ansible target) when the hosts do not reach it. */
    @Setting(omitempty = true)
    public String pgdg = "";
    @Setting(json = "pgdg_key", omitempty = true)
    public String pgdgKey = "";
    /** The extensions every database gets. */
    @Setting(json = "init_extensions", omitempty = true)
    public List<String> initExtensions = List.of();

    @Override
    public void setDefaults() {
        superuser.name = "postgres";
        conf.maxConnections = 100;
        conf.listenAddresses = "*";
        conf.port = 5432;
        pgdg = "https://apt.postgresql.org/pub/repos/apt";
        pgdgKey = "https://www.postgresql.org/media/keys/ACCC4CF8.asc";
        initExtensions = List.of("pg_stat_statements", "pgcrypto");
    }

    @Override
    public Meta meta() {
        // the directory whose content/ holds the files: where the program is run from, unless -Dpack.dir says
        return new Meta("postgres", "16.4").description("PostgreSQL").license("PostgreSQL")
                .image("images/postgres:16.4")
                .dir(System.getProperty("pack.dir", "."));
    }

    /** What an operator runs on the installed postgres besides an apply: a dump, which only reads, and a restore over
     *  the database, which the pack never lets run unasked. pg_dump and pg_restore reach the server by the instance's
     *  own socket, where the action runs; a dump under the data volume outlives it. */
    @Override
    public List<Action> packActions() {
        return List.of(
                new Action("backup", "pg_dump", "--username={{ .user }}", "--format={{ .format }}", "--file={{ .to }}", "--dbname={{ .database }}").timeout("1h")
                        .param(Action.Param.of("to", "string", true))
                        .param(Action.Param.of("format", "string", false).withDefault("custom").withEnum("custom", "plain", "directory"))
                        .param(Action.Param.of("database", "string", false).withDefault("postgres"))
                        .param(Action.Param.of("user", "string", false).withDefault("postgres")),
                new Action("restore", "pg_restore", "--username={{ .user }}", "--clean", "--if-exists", "--dbname={{ .database }}", "{{ .from }}").timeout("1h").mutating().approve()
                        .param(Action.Param.of("from", "string", true))
                        .param(Action.Param.of("database", "string", false).withDefault("postgres"))
                        .param(Action.Param.of("user", "string", false).withDefault("postgres")));
    }

    @Override
    public Root packRoot() {
        return new Root()
                // On a host the same application lives elsewhere, by OS; the version is the OS's, not Meta's
                .packages("debian", new Root.OSInstall()
                        .prepare("ca-certificates")
                        .repo(new Root.File("/etc/apt/keyrings/pgdg.asc").url("{{ .pgdg_key }}").mode("0644"))
                        .repo(new Root.File("/etc/apt/sources.list.d/pgdg.sources").template("host-pgdg.sources").mode("0644"))
                        .names("postgresql-16")
                        .content(new Root.File("/etc/postgresql/16/main/conf.d/pack.conf").template("host-postgresql.conf").mode("0644"))
                        .content(new Root.File("/etc/postgresql/16/main/pg_hba.conf").source("host-pg_hba.conf").mode("0640"))
                        .content(new Root.File("/opt/pack/init-extensions.sh").template("host-init.sh").mode("0700"))
                        .services("postgresql@16-main"))
                .packages("fedora", new Root.OSInstall()
                        .names("postgresql-server", "postgresql-contrib")
                        .command("sh", "-c", "test -f /var/lib/pgsql/data/PG_VERSION || postgresql-setup --initdb")
                        .content(new Root.File("/var/lib/pgsql/data/postgresql.auto.conf").template("host-postgresql.conf").mode("0644"))
                        .content(new Root.File("/var/lib/pgsql/data/pg_hba.conf").source("host-pg_hba.conf").mode("0644"))
                        .content(new Root.File("/opt/pack/init-extensions.sh").template("host-init.sh").mode("0700"))
                        .services("postgresql"))
                // the script of the install hook is rendered: a hook is a container of its own and is told where the server is
                .content(new Root.File("/etc/postgresql/postgresql.conf").values("conf").format("properties"))
                .content(new Root.File("/opt/pack/init-extensions.sh").template("init-extensions.sh").mode("0755"))
                .content(new Root.File("/opt/pack/restore-15.sh").source("restore-15.sh").mode("0755"))
                .port("pg", 5432)
                .data("data", new Root.Volume("/var/lib/postgresql/data").size("10Gi"))
                .minimum("cpu", "500m").minimum("memory", "512Mi")
                .readiness(Root.Readiness.tcp("pg"))
                .upgrade(new Root.Upgrade(">=15"))
                .hook(new Root.Hook("init-extensions", "postInstall", "/opt/pack/init-extensions.sh")
                        .image("new").idempotent().timeout("5m"))
                .hook(new Root.Hook("pg-upgrade", "preUpgrade", "/usr/lib/postgresql/16/bin/pg_upgrade",
                        "--old-bindir=/usr/lib/postgresql/15/bin", "--new-bindir=/usr/lib/postgresql/16/bin",
                        "--old-datadir=/var/lib/postgresql/data", "--new-datadir=/var/lib/postgresql/data.16")
                        .when(new Root.When().from("<16").to(">=16")).image("new").exclusive().timeout("30m")
                        .compensate(new Root.Step("/opt/pack/restore-15.sh").image("old")))
                .hook(new Root.Hook("analyze", "postUpgrade", "vacuumdb", "--all", "--analyze-in-stages")
                        .when(new Root.When().to(">=16")).image("new").idempotent());
    }

    /** A quarter of the memory limit, in MB: the formula fills the leaf when the operator did not write it. The key
     *  is the field's pointer, found from the field itself. */
    @Override
    public Map<String, String> packCompute() {
        return Map.of(Paths.pointer(Postgres.class, "conf", "sharedBuffers"),
                "has(limits.memory) ? string(limits.memory / 1048576 / 4) + 'MB' : '128MB'");
    }

    public static void main(String[] args) {
        System.exit(PackSdk.main(args, new Postgres()));
    }
}
