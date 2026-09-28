FROM alpine:3.22

RUN apk add --no-cache openssh-server busybox-extras \
 && adduser -D -s /bin/sh test \
 && passwd -u test \
 && mkdir -p /home/test/.ssh /www \
 && echo "hello from sandbox" > /www/index.html

COPY keys/server /etc/ssh/ssh_host_ed25519_key
COPY keys/client.pub /home/test/.ssh/authorized_keys
COPY sshd_config /etc/ssh/sshd_config

RUN chmod 600 /etc/ssh/ssh_host_ed25519_key \
 && chown -R test:test /home/test/.ssh \
 && chmod 700 /home/test/.ssh && chmod 600 /home/test/.ssh/authorized_keys

# busybox httpd gives local forwards something to talk to on port 8080
CMD ["sh", "-c", "httpd -p 127.0.0.1:8080 -h /www && exec /usr/sbin/sshd -D -e"]
