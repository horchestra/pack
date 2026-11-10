import io.horchestra.pack.sdk.Meta;
import io.horchestra.pack.sdk.Pack;
import io.horchestra.pack.sdk.PackSdk;
import io.horchestra.pack.sdk.Root;
import io.horchestra.pack.sdk.Setting;

/**
 * docker: a pack that makes a host a Docker host: the engine from Docker's own repositories
 * (https://docs.docker.com/engine/install/), its daemon.json and its unit. It has no image: it is for a target that
 * installs OS packages. The host is taken to be empty, nothing conflicting is looked for or removed. The same pack is
 * written in Go and Python next to this one; all three generate the same artifacts (examples/docker/golden).
 */
public class Docker extends Pack {
    public static class LogOpts {
        @Setting(json = "max-size", omitempty = true)
        public String maxSize = "";
        @Setting(json = "max-file", omitempty = true)
        public String maxFile = "";
    }

    public static class Daemon {
        @Setting(json = "log-driver", omitempty = true)
        public String logDriver = "";
        @Setting(json = "log-opts")
        public LogOpts logOpts = new LogOpts();
    }

    /** Where the packages come from: Docker's own by default, a mirror of the operator's (oci-packer proxy) when the hosts do not reach it. */
    @Setting(omitempty = true)
    public String mirror = "";
    public Daemon daemon = new Daemon();

    @Override
    public void setDefaults() {
        mirror = "https://download.docker.com/linux";
        daemon.logDriver = "json-file";
        daemon.logOpts.maxSize = "50m";
        daemon.logOpts.maxFile = "3";
    }

    @Override
    public Meta meta() {
        return new Meta("docker", "1.0.0").description("Docker Engine from OS packages").license("Apache-2.0");
    }

    private static final String[] ENGINE = {"docker-ce", "docker-ce-cli", "containerd.io", "docker-buildx-plugin", "docker-compose-plugin"};

    private static Root.File daemonJson() {
        return new Root.File("/etc/docker/daemon.json").values("daemon").format("json").mode("0644");
    }

    @Override
    public Root packRoot() {
        return new Root()
                .packages("debian", new Root.OSInstall()
                        .prepare("ca-certificates")
                        .repo(new Root.File("/etc/apt/keyrings/docker.asc").url("{{ .mirror }}/debian/gpg").mode("0644"))
                        .repo(new Root.File("/etc/apt/sources.list.d/docker.sources").template("docker.sources").mode("0644"))
                        .names(ENGINE).content(daemonJson()).services("docker"))
                .packages("fedora", new Root.OSInstall()
                        .repo(new Root.File("/etc/yum.repos.d/docker-ce.repo").template("docker-ce.repo").mode("0644"))
                        .names(ENGINE).content(daemonJson()).services("docker"));
    }

    public static void main(String[] args) {
        System.exit(PackSdk.main(args, new Docker()));
    }
}
