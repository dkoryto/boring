# Manual test sandbox

Two throwaway sshd containers for trying `boring` against real servers,
without touching your own `~/.boring.toml` or `~/.ssh/config`.

- `bastion` listens on `127.0.0.1:2222` on the host.
- `target` is only reachable through `bastion` (ProxyJump).
- Both run a small HTTP server on `127.0.0.1:8080` inside the container
  that answers `hello from sandbox`.

The containers reuse the host and client keys from `test/testdata/keys`,
so the generated `known_hosts` is static. Never use those keys anywhere
else, they are public.

## Usage

Requires Docker with the compose plugin.

```sh
make build
make sandbox-up
eval "$(./test/sandbox/sandbox.sh env)"   # points boring at run/

./dist/boring open -g sandbox
./dist/boring list
curl localhost:18080                               # local forward
curl localhost:18081                               # local forward via ProxyJump
curl --socks5-hostname localhost:11080 http://localhost:8080
curl --unix-socket test/sandbox/run/web.sock http://x/
./dist/boring close -a

make sandbox-down
```

The `back` tunnel is a remote forward: it exposes `localhost:18090` on
your machine as port 9000 inside the bastion. Start something on 18090,
then check it with:

```sh
docker exec sandbox-bastion-1 wget -qO- http://localhost:9000/
```

To test reconnection, open some tunnels and run
`docker restart sandbox-bastion-1`. The daemon log is at
`test/sandbox/run/boringd.log`.

Everything generated (`run/`, `keys/`) is ignored by git.
