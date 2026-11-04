FROM alpine:3.20
RUN apk add --no-cache openssh git \
 && ssh-keygen -A \
 && adduser -D git \
 && passwd -u git \
 && mkdir -p /home/git/.ssh \
 && chmod 700 /home/git/.ssh \
 && chown -R git:git /home/git/.ssh
EXPOSE 22
ENTRYPOINT ["/usr/sbin/sshd", "-D", "-e"]
