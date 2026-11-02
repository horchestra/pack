FROM debian:13
RUN apt-get update && apt-get install -y --no-install-recommends systemd systemd-sysv openssh-server python3 iproute2 iptables
RUN mkdir -p /root/.ssh /run/sshd && ssh-keygen -A && systemctl enable ssh
CMD ["/lib/systemd/systemd"]
