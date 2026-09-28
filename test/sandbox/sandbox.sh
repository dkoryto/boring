#!/bin/sh
# Manual test sandbox for boring. See README.md in this directory.
#
#   ./sandbox.sh up     build and start containers, write config to run/
#   ./sandbox.sh down   stop and remove containers
#   ./sandbox.sh env    print the environment variables to use the sandbox
set -eu

here=$(cd "$(dirname "$0")" && pwd)
testdata=$(cd "$here/../testdata" && pwd)
run="$here/run"

print_env() {
	cat <<EOT
export BORING_CONFIG="$run/boring.toml"
export BORING_SSH_CONFIG="$run/ssh_config"
export BORING_SOCK="$run/boringd.sock"
export BORING_LOG_FILE="$run/boringd.log"
EOT
}

up() {
	mkdir -p "$here/keys" "$run"
	cp "$testdata/keys/server" "$testdata/keys/client.pub" "$here/keys/"

	# The sandbox reuses the fixed test host key, so known_hosts can be
	# written up front instead of scanning the containers.
	hostkey=$(cut -d' ' -f1,2 "$testdata/keys/server.pub")
	cat > "$run/known_hosts" <<EOT
[127.0.0.1]:2222 $hostkey
target $hostkey
EOT

	cat > "$run/ssh_config" <<EOT
Host sandbox
    HostName 127.0.0.1
    Port 2222

Host sandbox-target
    HostName target
    Port 22
    ProxyJump sandbox

Host sandbox*
    User test
    IdentityFile $testdata/keys/client
    IdentitiesOnly yes
    UserKnownHostsFile $run/known_hosts
EOT

	cat > "$run/boring.toml" <<EOT
keep_alive = 30

[[tunnels]]
name = "web"
local = "18080"
remote = "localhost:8080"
host = "sandbox"
group = "sandbox"

[[tunnels]]
name = "web-jump"
local = "18081"
remote = "localhost:8080"
host = "sandbox-target"
group = "sandbox"

[[tunnels]]
name = "socks"
local = "11080"
mode = "socks"
host = "sandbox"
group = "sandbox"

[[tunnels]]
name = "web-unix"
local = "$run/web.sock"
remote = "localhost:8080"
host = "sandbox"
group = "sandbox"

# Exposes the host's localhost:18090 as port 9000 inside the bastion.
[[tunnels]]
name = "back"
mode = "remote"
local = "localhost:18090"
remote = "9000"
host = "sandbox"
group = "sandbox"
EOT

	docker compose -f "$here/compose.yml" up -d --build --wait
	echo
	echo "Sandbox is up. To use it in this shell:"
	echo "  eval \"\$($0 env)\""
}

down() {
	docker compose -f "$here/compose.yml" down --remove-orphans
}

case "${1:-}" in
up) up ;;
down) down ;;
env) print_env ;;
*)
	echo "usage: $0 up|down|env" >&2
	exit 1
	;;
esac
