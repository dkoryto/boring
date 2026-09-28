package tunnel

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The SSH config file used by prepare can only be overridden through the
// BORING_SSH_CONFIG environment variable, which the ssh_config package reads
// once at start-up. Tests calling prepare therefore re-run the test binary
// with the variable pointing to a config file written by the test.
const childEnv = "BORING_TUNNEL_TEST_CHILD"

// runWithSSHConfig re-runs the calling test in a child process which uses
// the given SSH config. It reports whether the caller is that child, in
// which case it should go on with the actual test.
func runWithSSHConfig(t *testing.T, config string) bool {
	t.Helper()
	if os.Getenv(childEnv) == t.Name() {
		return true
	}

	path := filepath.Join(t.TempDir(), "ssh_config")
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}

	args := []string{"-test.run=^" + t.Name() + "$", "-test.count=1", "-test.v"}
	// Let the child's coverage count towards the parent's
	if f := flag.Lookup("test.gocoverdir"); f != nil && f.Value.String() != "" {
		args = append(args, "-test.gocoverdir="+f.Value.String())
	}
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(),
		childEnv+"="+t.Name(),
		"BORING_SSH_CONFIG="+path,
		"SSH_AUTH_SOCK=",
		// Keep the race detector from pausing a second before exiting
		"GORACE="+strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: "+t.Name()) {
		t.Fatalf("child test failed: %v\n%s", err, out)
	}
	return false
}

func TestPrepareBadSSHConfig(t *testing.T) {
	if !runWithSSHConfig(t, "Host *\n  StrictHostKeyChecking bogus\n") {
		return
	}
	tun := FromDesc(&Desc{Name: "test", Host: "example.com"})
	expectErr(t, tun.prepare(), "could not parse SSH config")
}

const testSSHConfig = `Host *
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
`

func TestPrepareErrors(t *testing.T) {
	if !runWithSSHConfig(t, testSSHConfig) {
		return
	}
	valid := func() *Desc {
		return &Desc{
			Name:          "test",
			Host:          "127.0.0.1",
			User:          "test",
			Port:          "22",
			IdentityFile:  keysDir + "client",
			LocalAddress:  "8080",
			RemoteAddress: "localhost:8080",
		}
	}
	cases := []struct {
		name   string
		modify func(*Desc)
		msg    string
	}{
		{"port", func(d *Desc) { d.Port = "abc" }, "invalid port"},
		{"key", func(d *Desc) { d.IdentityFile = "missing" }, "no key files found"},
		{"remote", func(d *Desc) { d.RemoteAddress = "8080" }, "remote address"},
		{"local", func(d *Desc) { d.Mode = Remote; d.RemoteAddress = "8080" }, "local address"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := valid()
			c.modify(d)
			expectErr(t, FromDesc(d).prepare(), c.msg)
		})
	}
}

func TestPrepareAndForward(t *testing.T) {
	if !runWithSSHConfig(t, testSSHConfig) {
		return
	}
	s := startServer(t)
	keepAlive := 0
	tun := FromDesc(&Desc{
		Name:          "test",
		Host:          "127.0.0.1",
		User:          "test",
		Port:          StringOrInt(strconv.Itoa(s.port)),
		IdentityFile:  keysDir + "client",
		LocalAddress:  "127.0.0.1:0",
		RemoteAddress: StringOrInt(echoServer(t)),
		KeepAlive:     &keepAlive,
	})
	openOrFail(t, tun)
	if !tun.prepared {
		t.Fatal("tunnel not marked as prepared")
	}
}
