/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	gliderssh "github.com/gliderlabs/ssh"
	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
)

func TestLoadAuthorizedKeys(t *testing.T) {
	signer := newTestSigner(t)

	path := filepath.Join(t.TempDir(), "authorized_keys")
	if err := os.WriteFile(path, gossh.MarshalAuthorizedKey(signer.PublicKey()), 0o644); err != nil {
		t.Fatal(err)
	}

	keys, err := loadAuthorizedKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys))
	}
	if !bytes.Equal(keys[0], signer.PublicKey().Marshal()) {
		t.Fatal("loaded key does not match authorized key")
	}
}

func TestPublicKeyHandlerOnlyAllowsRoot(t *testing.T) {
	signer := newTestSigner(t)
	handler := publicKeyHandler([][]byte{signer.PublicKey().Marshal()})

	tests := []struct {
		user string
		want bool
	}{
		{user: "root", want: true},
		{user: "jovyan", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.user, func(t *testing.T) {
			ctx := &testSSHContext{Context: context.Background(), user: tt.user}
			if got := handler(ctx, signer.PublicKey()); got != tt.want {
				t.Fatalf("expected authorization for user %q to be %t, got %t", tt.user, tt.want, got)
			}
		})
	}
}

func newTestSigner(t *testing.T) gossh.Signer {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func TestLoadOrCreateHostSignerPersistsKey(t *testing.T) {
	dir := t.TempDir()

	first, err := loadOrCreateHostSigner(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOrCreateHostSigner(dir)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(first.PublicKey().Marshal(), second.PublicKey().Marshal()) {
		t.Fatal("host key was not persisted across loads")
	}

	info, err := os.Stat(filepath.Join(dir, "ssh_host_ed25519_key"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected host key mode 0600, got %o", info.Mode().Perm())
	}
}

func TestInstallSelfCopiesExecutable(t *testing.T) {
	assertInstallSelfCopiesExecutable(t)
}

func TestInstallSelfCopiesExecutableUnderRestrictiveUmask(t *testing.T) {
	// Umask is process-global, so this test must not run in parallel.
	previousUmask := syscall.Umask(0o027)
	defer syscall.Umask(previousUmask)

	assertInstallSelfCopiesExecutable(t)
}

func assertInstallSelfCopiesExecutable(t *testing.T) {
	t.Helper()
	destination := filepath.Join(t.TempDir(), "nested", "compute-ssh")
	if err := installSelf(destination); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Fatal("installed binary is empty")
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("expected installed binary mode 0755, got %o", info.Mode().Perm())
	}
}

func TestLoadConfigUsesCclusterEnv(t *testing.T) {
	t.Setenv("CCLUSTER_SSH_ADDR", ":2200")
	t.Setenv("SSH_PUBLIC_KEY_FILE", "/tmp/authorized_keys")
	t.Setenv("CCLUSTER_SSH_HOST_KEY_DIR", "/tmp/host-keys")
	t.Setenv("CCLUSTER_SSH_WORKDIR", "/workspace")

	cfg := loadConfig()

	if cfg.addr != ":2200" {
		t.Fatalf("expected addr %q, got %q", ":2200", cfg.addr)
	}
	if cfg.authorizedKeyFile != "/tmp/authorized_keys" {
		t.Fatalf("expected authorized key file %q, got %q", "/tmp/authorized_keys", cfg.authorizedKeyFile)
	}
	if cfg.hostKeyDir != "/tmp/host-keys" {
		t.Fatalf("expected host key dir %q, got %q", "/tmp/host-keys", cfg.hostKeyDir)
	}
	if cfg.workingDir != "/workspace" {
		t.Fatalf("expected working dir %q, got %q", "/workspace", cfg.workingDir)
	}
}

func TestNewRemoteCommandUsesNonLoginShell(t *testing.T) {
	cmd := newRemoteCommand(shellSpec{path: "/bin/sh", name: "sh"}, "printf ok")
	want := []string{"/bin/sh", "-c", "printf ok"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("expected args %#v, got %#v", want, cmd.Args)
	}
}

func TestBuildEnvSetsRootSessionIdentity(t *testing.T) {
	tests := []struct {
		name       string
		baseEnv    []string
		requestEnv []string
	}{
		{
			name:    "shell is absent",
			baseEnv: []string{"PATH=/bin"},
		},
		{
			name:    "base environment has a different shell",
			baseEnv: []string{"PATH=/bin", "SHELL=/bin/false"},
		},
		{
			name:       "client requests a different shell",
			baseEnv:    []string{"PATH=/bin", "SHELL=/bin/false", "HOME=/home/jovyan", "USER=jovyan"},
			requestEnv: []string{"LANG=C", "SHELL=/usr/bin/false", "LOGNAME=jovyan"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			session := newBlockingInputSession()
			session.environ = tt.requestEnv

			env := buildEnv(session, shellSpec{path: "/bin/sh", name: "sh"}, "", tt.baseEnv...)

			want := map[string]string{
				"SHELL":   "/bin/sh",
				"HOME":    "/root",
				"USER":    "root",
				"LOGNAME": "root",
			}
			for key, value := range want {
				var values []string
				for _, entry := range env {
					if after, ok := strings.CutPrefix(entry, key+"="); ok {
						values = append(values, after)
					}
				}
				if expected := []string{value}; !reflect.DeepEqual(values, expected) {
					t.Fatalf("expected %s values %#v, got %#v", key, expected, values)
				}
			}
		})
	}
}

func TestRunRemoteCommandExportsSelectedShell(t *testing.T) {
	session := newBlockingInputSession()
	defer session.closeRead()
	session.environ = []string{"SHELL=/bin/false"}
	session.rawCommand = `printf %s "$SHELL"`

	runRemoteCommand(
		config{
			workingDir:      t.TempDir(),
			shellCandidates: []shellSpec{{path: "/bin/sh", name: "sh"}},
		},
		session,
		"",
	)

	if got := session.stdout.String(); got != "/bin/sh" {
		t.Fatalf("expected exported SHELL %q, got %q", "/bin/sh", got)
	}
}

func TestSFTPWorksWhenNoShellIsAvailable(t *testing.T) {
	clientSigner := newTestSigner(t)
	hostSigner := newTestSigner(t)
	rootDir := t.TempDir()
	missingShell := filepath.Join(rootDir, "missing-shell")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	server := newSSHServer(
		config{
			addr:            listener.Addr().String(),
			workingDir:      rootDir,
			shellCandidates: []shellSpec{{path: missingShell, name: "missing-shell"}},
		},
		[][]byte{clientSigner.PublicKey().Marshal()},
		hostSigner,
	)
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(listener)
	}()
	defer func() {
		_ = server.Close()
		select {
		case err := <-serveDone:
			if !errors.Is(err, gliderssh.ErrServerClosed) {
				t.Errorf("SSH server stopped with %v, want %v", err, gliderssh.ErrServerClosed)
			}
		case <-time.After(time.Second):
			t.Fatal("SSH server did not stop")
		}
	}()

	client, err := gossh.Dial("tcp", listener.Addr().String(), &gossh.ClientConfig{
		User:            "root",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(clientSigner)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()

	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	output, err := session.CombinedOutput("printf should-not-run")
	if err == nil {
		t.Fatal("expected remote command to fail without a shell")
	}
	exitErr, ok := err.(*gossh.ExitError)
	if !ok {
		t.Fatalf("expected SSH exit error, got %T: %v", err, err)
	}
	if exitErr.ExitStatus() != 127 {
		t.Fatalf("expected shell failure exit status 127, got %d", exitErr.ExitStatus())
	}
	if !strings.Contains(string(output), "unsupported image for SSH shell access") {
		t.Fatalf("expected unsupported shell message, got %q", string(output))
	}

	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sftpClient.Close() }()

	probePath := filepath.Join(rootDir, "sftp-probe.txt")
	file, err := sftpClient.Create(probePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("sftp works")); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(probePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "sftp works" {
		t.Fatalf("expected SFTP-written content %q, got %q", "sftp works", string(got))
	}
}

func TestChooseShellFrom(t *testing.T) {
	dir := t.TempDir()
	bashPath := writeShellCandidate(t, dir, "bash", 0o755)
	shPath := writeShellCandidate(t, dir, "sh", 0o755)
	nonExecutablePath := writeShellCandidate(t, dir, "not-executable", 0o644)
	directoryPath := filepath.Join(dir, "directory")
	if err := os.Mkdir(directoryPath, 0o755); err != nil {
		t.Fatal(err)
	}
	missingPath := filepath.Join(dir, "missing")

	tests := []struct {
		name       string
		candidates []shellSpec
		want       shellSpec
		wantErr    bool
	}{
		{
			name: "prefers first executable bash candidate",
			candidates: []shellSpec{
				{path: bashPath, name: "bash"},
				{path: shPath, name: "sh"},
			},
			want: shellSpec{path: bashPath, name: "bash"},
		},
		{
			name: "falls back to executable sh candidate",
			candidates: []shellSpec{
				{path: missingPath, name: "bash"},
				{path: shPath, name: "sh"},
			},
			want: shellSpec{path: shPath, name: "sh"},
		},
		{
			name: "skips directories and non-executable files",
			candidates: []shellSpec{
				{path: directoryPath, name: "directory"},
				{path: nonExecutablePath, name: "not-executable"},
				{path: shPath, name: "sh"},
			},
			want: shellSpec{path: shPath, name: "sh"},
		},
		{
			name: "returns clear error when no shell is available",
			candidates: []shellSpec{
				{path: missingPath, name: "missing"},
				{path: nonExecutablePath, name: "not-executable"},
				{path: directoryPath, name: "directory"},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := chooseShellFrom(tt.candidates)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if err.Error() != "no supported shell found; expected /bin/bash or /bin/sh" {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("expected %+v, got %+v", tt.want, got)
			}
		})
	}
}

func writeShellCandidate(t *testing.T, dir string, name string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunCommandWithSessionIOReturnsBeforeSessionInputEOF(t *testing.T) {
	session := newBlockingInputSession()
	defer session.closeRead()

	result := make(chan int, 1)
	go func() {
		cmd := exec.Command("/bin/sh", "-c", "printf done")
		result <- runCommandWithSessionIO(session.Context(), session, cmd)
	}()

	select {
	case exitCode := <-result:
		if exitCode != 0 {
			t.Fatalf("expected exit code 0, got %d", exitCode)
		}
		if got := session.stdout.String(); got != "done" {
			t.Fatalf("expected stdout %q, got %q", "done", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("command did not return while session input remained open")
	}
}

type blockingInputSession struct {
	ctx        gliderssh.Context
	readDone   chan struct{}
	closeOnce  sync.Once
	stdout     bytes.Buffer
	stderr     bytes.Buffer
	environ    []string
	rawCommand string
}

func newBlockingInputSession() *blockingInputSession {
	return &blockingInputSession{
		ctx:      &testSSHContext{Context: context.Background()},
		readDone: make(chan struct{}),
	}
}

func (s *blockingInputSession) Read(_ []byte) (int, error) {
	<-s.readDone
	return 0, io.EOF
}

func (s *blockingInputSession) Write(p []byte) (int, error) {
	return s.stdout.Write(p)
}

func (s *blockingInputSession) Close() error {
	s.closeRead()
	return nil
}

func (s *blockingInputSession) CloseWrite() error {
	s.closeRead()
	return nil
}

func (s *blockingInputSession) closeRead() {
	s.closeOnce.Do(func() {
		close(s.readDone)
	})
}

func (s *blockingInputSession) SendRequest(_ string, _ bool, _ []byte) (bool, error) {
	return false, nil
}

func (s *blockingInputSession) Stderr() io.ReadWriter {
	return &s.stderr
}

func (s *blockingInputSession) User() string {
	return "test-user"
}

func (s *blockingInputSession) RemoteAddr() net.Addr {
	return nil
}

func (s *blockingInputSession) LocalAddr() net.Addr {
	return nil
}

func (s *blockingInputSession) Environ() []string {
	return s.environ
}

func (s *blockingInputSession) Exit(_ int) error {
	return nil
}

func (s *blockingInputSession) Command() []string {
	return nil
}

func (s *blockingInputSession) RawCommand() string {
	return s.rawCommand
}

func (s *blockingInputSession) Subsystem() string {
	return ""
}

func (s *blockingInputSession) PublicKey() gliderssh.PublicKey {
	return nil
}

func (s *blockingInputSession) Context() gliderssh.Context {
	return s.ctx
}

func (s *blockingInputSession) Permissions() gliderssh.Permissions {
	return gliderssh.Permissions{}
}

func (s *blockingInputSession) Pty() (gliderssh.Pty, <-chan gliderssh.Window, bool) {
	return gliderssh.Pty{}, nil, false
}

func (s *blockingInputSession) Signals(_ chan<- gliderssh.Signal) {}

func (s *blockingInputSession) Break(_ chan<- bool) {}

type testSSHContext struct {
	context.Context
	mu   sync.Mutex
	user string
}

func (c *testSSHContext) Lock() {
	c.mu.Lock()
}

func (c *testSSHContext) Unlock() {
	c.mu.Unlock()
}

func (c *testSSHContext) User() string {
	if c.user != "" {
		return c.user
	}
	return "test-user"
}

func (c *testSSHContext) SessionID() string {
	return "test-session"
}

func (c *testSSHContext) ClientVersion() string {
	return "test-client"
}

func (c *testSSHContext) ServerVersion() string {
	return "test-server"
}

func (c *testSSHContext) RemoteAddr() net.Addr {
	return nil
}

func (c *testSSHContext) LocalAddr() net.Addr {
	return nil
}

func (c *testSSHContext) Permissions() *gliderssh.Permissions {
	return &gliderssh.Permissions{}
}

func (c *testSSHContext) SetValue(_, _ any) {}
