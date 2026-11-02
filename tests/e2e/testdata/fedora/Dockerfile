FROM fedora:43
RUN dnf install -y --setopt=install_weak_deps=False systemd openssh-server python3 python3-libdnf5 iproute iptables-nft
RUN mkdir -p /root/.ssh && ssh-keygen -A && systemctl enable sshd
CMD ["/usr/lib/systemd/systemd"]
