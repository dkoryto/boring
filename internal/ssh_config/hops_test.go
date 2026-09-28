package ssh_config

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// testAgent is an in-process ssh-agent served on SSH_AUTH_SOCK for the whole
// test binary. The agent package caches its client, so all tests share it and
// adjust its contents via useAgentKeys.
var testAgent = agent.NewKeyring()

func TestMain(m *testing.M) {
	os.Exit(runWithAgent(m))
}

func runWithAgent(m *testing.M) int {
	// Unix socket paths are limited to 104 bytes on macOS
	dir, err := os.MkdirTemp("", "ag")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	l, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		panic(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = agent.ServeAgent(testAgent, c)
			}()
		}
	}()

	os.Setenv("SSH_AUTH_SOCK", l.Addr().String())
	return m.Run()
}

// useAgentKeys replaces the agent's contents for the duration of a test.
func useAgentKeys(t *testing.T, keys ...agent.AddedKey) {
	t.Helper()
	if err := testAgent.RemoveAll(); err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if err := testAgent.Add(k); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = testAgent.RemoveAll() })
}

// useConfig writes an ssh config to a temp file and points ParseSSHConfig at it.
func useConfig(t *testing.T, content string) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(cfg, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	old := overrideConfig
	overrideConfig = cfg
	t.Cleanup(func() { overrideConfig = old })
}

func genKey(t *testing.T) (ed25519.PrivateKey, ssh.Signer) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return priv, s
}

// signCert issues a user certificate for pub, signed by a fresh CA.
func signCert(t *testing.T, pub ssh.PublicKey) *ssh.Certificate {
	t.Helper()
	_, ca := genKey(t)
	c := &ssh.Certificate{
		Key:             pub,
		CertType:        ssh.UserCert,
		KeyId:           "test",
		ValidPrincipals: []string{"bob"},
		ValidBefore:     ssh.CertTimeInfinity,
	}
	if err := c.SignCert(rand.Reader, ca); err != nil {
		t.Fatal(err)
	}
	return c
}

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseSSHConfigErrors(t *testing.T) {
	cases := []struct {
		name, config, want string
	}{
		{"strict host key checking", "Host h\n\tStrictHostKeyChecking maybe\n", "unsupported StrictHostKeyChecking"},
		{"proxy jump port", "Host h\n\tProxyJump j:notaport\n", "could not parse jump host"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useConfig(t, tc.config)
			_, err := ParseSSHConfig("h", "bob")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestParseSSHConfigMissingFile(t *testing.T) {
	old := overrideConfig
	overrideConfig = filepath.Join(t.TempDir(), "missing")
	t.Cleanup(func() { overrideConfig = old })

	if _, err := ParseSSHConfig("h", "bob"); err == nil {
		t.Fatal("expected error for missing config file")
	}
}

// accept-new is not supported and falls back to strict checking
func TestParseSSHConfigAcceptNew(t *testing.T) {
	useConfig(t, "Host h\n\tStrictHostKeyChecking accept-new\n")
	sc, err := ParseSSHConfig("h", "bob")
	if err != nil {
		t.Fatal(err)
	}
	if sc.KeyCheck != strict {
		t.Fatalf("KeyCheck = %v, want strict", sc.KeyCheck)
	}
}

func TestParseProxyJump(t *testing.T) {
	cases := []struct {
		in   string
		want jumpSpec
	}{
		{"host", jumpSpec{host: "host"}},
		{"alice@host", jumpSpec{host: "host", user: "alice"}},
		{"alice@host:2200", jumpSpec{host: "host", user: "alice", port: 2200}},
	}
	for _, tc := range cases {
		got, err := parseProxyJump(tc.in)
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if *got != tc.want {
			t.Errorf("%q: got %+v, want %+v", tc.in, *got, tc.want)
		}
	}
	if _, err := parseProxyJump("host:x"); err == nil {
		t.Fatal("expected error for non-numeric port")
	}
}

func TestDummyKey(t *testing.T) {
	var k dummyKey
	if k.Type() != "dummy" {
		t.Errorf("Type = %q", k.Type())
	}
	if k.Marshal() != nil {
		t.Error("Marshal should return nil")
	}
	if k.Verify(nil, nil) == nil {
		t.Error("Verify should fail")
	}
}

// Wanted known_hosts entries without a key must be skipped.
func TestExtractHostKeyAlgosNilKey(t *testing.T) {
	ed := edPub(t)
	cb := func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if _, ok := key.(dummyKey); ok {
			return &knownhosts.KeyError{Want: []knownhosts.KnownKey{{}, {Key: ed}}}
		}
		return errors.New("no authorities for hostname")
	}
	got := extractHostKeyAlgos(cb, testHostPort)
	if want := []string{ssh.KeyAlgoED25519}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		sc   SSHConfig
		want string
	}{
		{SSHConfig{User: "u", Port: 22}, "no host"},
		{SSHConfig{HostName: "h", Port: 22}, "no user"},
		{SSHConfig{HostName: "h", User: "u"}, "no port"},
		{SSHConfig{HostName: "h", User: "u", Port: 22}, ""},
	}
	for _, tc := range cases {
		err := tc.sc.validate()
		if tc.want == "" {
			if err != nil {
				t.Errorf("%+v: unexpected error %v", tc.sc, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: got %v, want %q", tc.sc, err, tc.want)
		}
	}
}

func TestMakeCallbackAndAlgosErrors(t *testing.T) {
	base := SSHConfig{
		Alias:        "h",
		HostName:     "127.0.0.1",
		Port:         2222,
		HostKeyAlgos: []string{ssh.KeyAlgoED25519},
	}

	t.Run("malformed known_hosts", func(t *testing.T) {
		sc := base
		sc.KnownHostsFiles = []string{writeFile(t, "known_hosts", []byte("@bogus-marker\n"))}
		if _, _, err := sc.makeCallbackAndAlgos(); err == nil ||
			!strings.Contains(err.Error(), "knownhosts") {
			t.Fatalf("got %v, want knownhosts error", err)
		}
	})

	t.Run("host unknown", func(t *testing.T) {
		sc := base
		sc.KnownHostsFiles = []string{
			writeFile(t, "known_hosts", nil),
			filepath.Join(t.TempDir(), "missing"),
		}
		if _, _, err := sc.makeCallbackAndAlgos(); err == nil ||
			!strings.Contains(err.Error(), "could not determine host key algorithms") {
			t.Fatalf("got %v, want host key algorithm error", err)
		}
	})

	t.Run("checking off", func(t *testing.T) {
		sc := base
		sc.KeyCheck = off
		cb, algs, err := sc.makeCallbackAndAlgos()
		if err != nil {
			t.Fatal(err)
		}
		if cb == nil {
			t.Fatal("nil callback")
		}
		if cb("any", &net.TCPAddr{}, edPub(t)) != nil {
			t.Fatal("callback should accept any host key")
		}
		if !reflect.DeepEqual(algs, sc.HostKeyAlgos) {
			t.Fatalf("got %v, want %v", algs, sc.HostKeyAlgos)
		}
	})
}

func TestLoadKeyEmptyPath(t *testing.T) {
	if _, err := loadPrivateKey(""); err == nil {
		t.Error("loadPrivateKey: expected error")
	}
	if _, err := loadPublicKey(""); err == nil {
		t.Error("loadPublicKey: expected error")
	}
	if _, _, ok := loadIdentity(""); ok {
		t.Error("loadIdentity: expected failure")
	}
}

// An IdentityFile pointing at a certificate is fingerprinted by its key
func TestLoadIdentityCertificate(t *testing.T) {
	_, s := genKey(t)
	c := signCert(t, s.PublicKey())
	p := writeFile(t, "id-cert.pub", ssh.MarshalAuthorizedKey(c))

	sig, fp, ok := loadIdentity(p)
	if !ok || sig != nil {
		t.Fatalf("got signer=%v ok=%v, want nil signer and ok", sig, ok)
	}
	if fp != keyFP(s.PublicKey()) {
		t.Fatal("fingerprint does not match certified key")
	}
}

func TestLoadCert(t *testing.T) {
	_, s := genKey(t)
	c := signCert(t, s.PublicKey())

	got, err := loadCert(writeFile(t, "cert.pub", ssh.MarshalAuthorizedKey(c)))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Key.Marshal(), s.PublicKey().Marshal()) {
		t.Fatal("certificate key mismatch")
	}

	bad := map[string]string{
		"missing":   filepath.Join(t.TempDir(), "missing"),
		"garbage":   writeFile(t, "garbage", []byte("garbage\n")),
		"plain key": writeFile(t, "plain.pub", ssh.MarshalAuthorizedKey(s.PublicKey())),
	}
	for name, p := range bad {
		if _, err := loadCert(p); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestCertify(t *testing.T) {
	_, s := genKey(t)
	_, other := genKey(t)
	c := signCert(t, s.PublicKey())

	cs, err := certify(c, s)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cs.PublicKey().(*ssh.Certificate); !ok {
		t.Fatal("expected certificate public key")
	}
	if _, err := certify(c, cs); err == nil {
		t.Error("expected error for signer that is already a certificate")
	}
	if _, err := certify(c, other); err == nil {
		t.Error("expected error for mismatching signer")
	}
}

func TestDedupeSigners(t *testing.T) {
	_, a := genKey(t)
	_, b := genKey(t)
	got := dedupeSigners([]ssh.Signer{a, b, a})
	if len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("got %v, want [a b]", got)
	}
}

func TestToHopsDepthExceeded(t *testing.T) {
	sc := &SSHConfig{Alias: "h"}
	if _, err := sc.toHopsImpl(false, maxJumpRecursions+1); err == nil {
		t.Fatal("expected recursion error")
	}
}

func TestToHopsInvalid(t *testing.T) {
	sc := &SSHConfig{Alias: "h", User: "bob", Port: 22}
	if _, err := sc.ToHops(); err == nil || !strings.Contains(err.Error(), "no host") {
		t.Fatalf("got %v, want validation error", err)
	}
}

func TestToHopsJumps(t *testing.T) {
	dir := t.TempDir()
	key, _ := writeKeyPair(t, dir, "id")
	useAgentKeys(t)
	useConfig(t, strings.Join([]string{
		"Host target",
		"\tHostName target.example",
		"\tUser bob",
		"\tProxyJump j1,alice@j2:2200",
		"Host j1",
		"\tHostName j1.example",
		"\tProxyJump j0",
		"Host *",
		"\tStrictHostKeyChecking no",
		"\tIdentitiesOnly yes",
		"\tIdentityFile " + key,
		"",
	}, "\n"))

	sc, err := ParseSSHConfig("target", "bob")
	if err != nil {
		t.Fatal(err)
	}
	hops, err := sc.ToHops()
	if err != nil {
		t.Fatal(err)
	}

	// j1 is reached via j0; the jumps of j2 would be ignored, like ssh(1)
	type hop struct {
		host string
		port int
		user string
	}
	var got []hop
	for _, h := range hops {
		got = append(got, hop{h.HostName, h.Port, h.User})
	}
	want := []hop{
		{"j0", 22, got[0].user}, // user defaults to $USER
		{"j1.example", 22, got[1].user},
		{"j2", 2200, "alice"},
		{"target.example", 22, "bob"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestToHopsJumpErrors(t *testing.T) {
	dir := t.TempDir()
	key, _ := writeKeyPair(t, dir, "id")
	useAgentKeys(t)
	useConfig(t, strings.Join([]string{
		"Host badjump",
		"\tStrictHostKeyChecking maybe",
		"Host nokeys",
		"\tIdentityFile " + filepath.Join(dir, "missing"),
		"Host *",
		"\tStrictHostKeyChecking no",
		"\tIdentitiesOnly yes",
		"",
	}, "\n"))

	cases := []struct {
		jump, want string
	}{
		{"badjump", "could not parse SSH config for badjump"},
		{"nokeys", "no key files found"},
	}
	for _, tc := range cases {
		t.Run(tc.jump, func(t *testing.T) {
			sc := &SSHConfig{
				Alias:         "target",
				HostName:      "target.example",
				User:          "bob",
				Port:          22,
				KeyCheck:      off,
				IdentityFiles: []string{key},
				Jumps:         []*jumpSpec{{host: tc.jump}},
			}
			if _, err := sc.ToHops(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestToHopsUnknownHost(t *testing.T) {
	key, _ := writeKeyPair(t, t.TempDir(), "id")
	useAgentKeys(t)
	sc := &SSHConfig{
		Alias:           "target",
		HostName:        "target.example",
		User:            "bob",
		Port:            22,
		IdentityFiles:   []string{key},
		KnownHostsFiles: []string{writeFile(t, "known_hosts", nil)},
		HostKeyAlgos:    []string{ssh.KeyAlgoED25519},
	}
	if _, err := sc.ToHops(); err == nil ||
		!strings.Contains(err.Error(), "could not determine host key algorithms") {
		t.Fatalf("got %v, want host key algorithm error", err)
	}
}

// Keys are ordered like OpenSSH: agent keys matching IdentityFiles, then
// other agent keys, then key files. An agent key that is also an IdentityFile
// is only offered once.
func TestMakeSignersWithAgent(t *testing.T) {
	dir := t.TempDir()

	// Configured key, present both as a file and in the agent
	cfgPriv, cfgSig := genKey(t)
	cfgPath := writeFile(t, "id_cfg", pemKey(t, cfgPriv))
	// Key only in the agent, not configured
	otherPriv, otherSig := genKey(t)
	// Key only on disk, not in the agent
	filePath, _ := writeKeyPair(t, dir, "id_file")
	fileSig, err := loadPrivateKey(filePath)
	if err != nil {
		t.Fatal(err)
	}

	useAgentKeys(t,
		agent.AddedKey{PrivateKey: cfgPriv},
		agent.AddedKey{PrivateKey: otherPriv},
	)

	sc := &SSHConfig{Alias: "h", IdentityFiles: []string{cfgPath, filePath}}
	sigs, err := sc.makeSigners()
	if err != nil {
		t.Fatal(err)
	}

	want := [][]byte{
		cfgSig.PublicKey().Marshal(),
		otherSig.PublicKey().Marshal(),
		fileSig.PublicKey().Marshal(),
	}
	if len(sigs) != len(want) {
		t.Fatalf("got %d signers, want %d", len(sigs), len(want))
	}
	for i, s := range sigs {
		if string(s.PublicKey().Marshal()) != string(want[i]) {
			t.Errorf("signer %d: unexpected key", i)
		}
	}

	// With IdentitiesOnly, unrelated agent keys are dropped
	sc.IdentitiesOnly = true
	sigs, err = sc.makeSigners()
	if err != nil {
		t.Fatal(err)
	}
	if len(sigs) != 2 ||
		string(sigs[0].PublicKey().Marshal()) != string(cfgSig.PublicKey().Marshal()) ||
		string(sigs[1].PublicKey().Marshal()) != string(fileSig.PublicKey().Marshal()) {
		t.Fatalf("IdentitiesOnly: got %v", sigs)
	}
}

// A CertificateFile is bound to the matching private key; unreadable
// certificate files are skipped.
func TestMakeSignersCertificateFile(t *testing.T) {
	useAgentKeys(t)
	path, _ := writeKeyPair(t, t.TempDir(), "id")
	s, err := loadPrivateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	c := signCert(t, s.PublicKey())

	sc := &SSHConfig{
		Alias:         "h",
		IdentityFiles: []string{path},
		CertificateFiles: []string{
			filepath.Join(t.TempDir(), "missing-cert.pub"),
			writeFile(t, "cert.pub", ssh.MarshalAuthorizedKey(c)),
		},
	}
	sigs, err := sc.makeSigners()
	if err != nil {
		t.Fatal(err)
	}
	if len(sigs) != 2 {
		t.Fatalf("got %d signers, want 2", len(sigs))
	}
	if _, ok := sigs[0].PublicKey().(*ssh.Certificate); !ok {
		t.Fatal("first signer should be the certificate")
	}
}

func pemKey(t *testing.T, priv ed25519.PrivateKey) []byte {
	t.Helper()
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(block)
}

// Without CertificateFile, a sibling <IdentityFile>-cert.pub is picked up.
func TestMakeSignersImplicitCertificate(t *testing.T) {
	useAgentKeys(t)
	path, _ := writeKeyPair(t, t.TempDir(), "id")
	s, err := loadPrivateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	c := signCert(t, s.PublicKey())
	if err := os.WriteFile(path+"-cert.pub", ssh.MarshalAuthorizedKey(c), 0o600); err != nil {
		t.Fatal(err)
	}

	sc := &SSHConfig{Alias: "h", IdentityFiles: []string{path}}
	sigs, err := sc.makeSigners()
	if err != nil {
		t.Fatal(err)
	}
	if len(sigs) != 2 {
		t.Fatalf("got %d signers, want 2", len(sigs))
	}
	if _, ok := sigs[0].PublicKey().(*ssh.Certificate); !ok {
		t.Fatal("first signer should be the certificate")
	}
}

// A certificate held by the agent must be recognized as such, even though
// the agent client hands out its keys as *agent.Key. With IdentitiesOnly it
// has to be kept when the configured identity is its underlying key.
func TestMakeSignersAgentCertificate(t *testing.T) {
	priv, s := genKey(t)
	c := signCert(t, s.PublicKey())
	useAgentKeys(t, agent.AddedKey{PrivateKey: priv, Certificate: c})

	pubPath := writeFile(t, "id.pub", ssh.MarshalAuthorizedKey(s.PublicKey()))
	sc := &SSHConfig{Alias: "h", IdentityFiles: []string{pubPath}, IdentitiesOnly: true}
	sigs, err := sc.makeSigners()
	if err != nil {
		t.Fatal(err)
	}
	if len(sigs) != 1 {
		t.Fatalf("got %d signers, want 1", len(sigs))
	}
	if string(sigs[0].PublicKey().Marshal()) != string(c.Marshal()) {
		t.Fatal("signer is not the agent's certificate")
	}

	// A certificate for a key that isn't configured is dropped
	_, other := genKey(t)
	sc.IdentityFiles = []string{writeFile(t, "other.pub", ssh.MarshalAuthorizedKey(other.PublicKey()))}
	if sigs, err = sc.makeSigners(); err == nil && len(sigs) != 0 {
		t.Fatalf("got %d signers, want none", len(sigs))
	}
}

func TestAsCert(t *testing.T) {
	_, s := genKey(t)
	c := signCert(t, s.PublicKey())

	if got, ok := asCert(c); !ok || string(got.Marshal()) != string(c.Marshal()) {
		t.Error("certificate not recognized")
	}
	// What the agent client returns for a certificate identity
	wrapped := &agent.Key{Format: c.Type(), Blob: c.Marshal()}
	if got, ok := asCert(wrapped); !ok || string(got.Marshal()) != string(c.Marshal()) {
		t.Error("certificate from agent not recognized")
	}
	if _, ok := asCert(s.PublicKey()); ok {
		t.Error("plain key taken for a certificate")
	}
	if _, ok := asCert(dummyKey{}); ok {
		t.Error("unparsable key taken for a certificate")
	}
}
