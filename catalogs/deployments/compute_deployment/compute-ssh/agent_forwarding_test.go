/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package main

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gliderssh "github.com/gliderlabs/ssh"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

//nolint:gocyclo // The subtests share one server and run in parallel on purpose.
func TestAgentForwarding(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "")
	loginKey := newTestSigner(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := newSSHServer(config{
		workingDir:      t.TempDir(),
		shellCandidates: []shellSpec{{path: "/bin/sh", name: "sh"}},
	}, [][]byte{loginKey.PublicKey().Marshal()}, newTestSigner(t))
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		select {
		case err := <-serveDone:
			if !errors.Is(err, gliderssh.ErrServerClosed) {
				t.Errorf("SSH server stopped with %v, want %v", err, gliderssh.ErrServerClosed)
			}
		case <-time.After(time.Second):
			t.Error("SSH server did not stop")
		}
	})

	for _, tt := range []struct {
		name       string
		forward    bool
		shell      bool
		disconnect bool
	}{
		{name: "disabled"},
		{name: "command", forward: true},
		{name: "shell", forward: true, shell: true},
		{name: "disconnect", forward: true, disconnect: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Run clients together to check that each connection uses its own agent.
			t.Parallel()
			conn, err := net.DialTimeout("tcp", listener.Addr().String(), 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
				t.Fatal(err)
			}
			sshConn, channels, requests, err := gossh.NewClientConn(conn, listener.Addr().String(), &gossh.ClientConfig{
				User:            "root",
				Auth:            []gossh.AuthMethod{gossh.PublicKeys(loginKey)},
				HostKeyCallback: gossh.InsecureIgnoreHostKey(),
			})
			if err != nil {
				t.Fatal(err)
			}
			client := gossh.NewClient(sshConn, channels, requests)
			t.Cleanup(func() { _ = client.Close() })
			session, err := client.NewSession()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close() }()

			_, privateKey, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			publicKey, err := gossh.NewPublicKey(privateKey.Public())
			if err != nil {
				t.Fatal(err)
			}
			if tt.forward {
				keyring := agent.NewKeyring()
				if err := keyring.Add(agent.AddedKey{PrivateKey: privateKey}); err != nil {
					t.Fatal(err)
				}
				if err := agent.ForwardToAgent(client, keyring); err != nil {
					t.Fatal(err)
				}
				if err := agent.RequestAgentForwarding(session); err != nil {
					t.Fatal(err)
				}
				if err := session.Setenv("SSH_AUTH_SOCK", "/invalid-client-socket"); err != nil {
					t.Fatal(err)
				}
			}
			type sessionResult struct {
				socket string
				err    error
			}
			otherSession := make(chan sessionResult, 1)
			if tt.forward {
				// Start both sessions together. Only the first requested forwarding.
				go func() {
					other, err := client.NewSession()
					if err != nil {
						otherSession <- sessionResult{err: err}
						return
					}
					defer func() { _ = other.Close() }()
					output, err := other.CombinedOutput(`printf '%s' "$SSH_AUTH_SOCK"`)
					otherSession <- sessionResult{socket: string(output), err: err}
				}()
			}
			stdout, err := session.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdin, err := session.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			probe := `printf '\nSSH_AUTH_SOCK=%s\n' "$SSH_AUTH_SOCK"; read finish`
			if tt.shell {
				if err := session.Shell(); err != nil {
					t.Fatal(err)
				}
				if _, err := io.WriteString(stdin, probe+"\n"); err != nil {
					t.Fatal(err)
				}
			} else if err := session.Start(probe); err != nil {
				t.Fatal(err)
			}

			socketPath := ""
			found := false
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				line := strings.TrimSuffix(scanner.Text(), "\r")
				if after, ok := strings.CutPrefix(line, "SSH_AUTH_SOCK="); ok {
					socketPath = after
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("command did not report SSH_AUTH_SOCK: %v", scanner.Err())
			}
			var activeAgent agent.Agent
			var agentConn net.Conn
			if tt.forward {
				other := <-otherSession
				if other.err != nil {
					t.Fatal(other.err)
				}
				if other.socket != socketPath {
					t.Fatalf("sessions on one connection use different agent sockets: %q and %q", socketPath, other.socket)
				}
				activeAgent, agentConn = checkForwardedAgent(t, socketPath, publicKey)
			} else if socketPath != "" {
				t.Fatalf("unexpected agent socket without a forwarding request: %q", socketPath)
			}

			if tt.disconnect {
				_ = client.Close()
			} else {
				if _, err := io.WriteString(stdin, "\nexit\n"); err != nil {
					t.Fatal(err)
				}
				if err := session.Wait(); err != nil {
					t.Fatal(err)
				}
				if tt.forward {
					checkAgentSignature(t, activeAgent, publicKey)
					// New agent connections must also work after a shell exits.
					checkForwardedAgent(t, socketPath, publicKey)
				}
				_ = client.Close()
			}
			if tt.forward {
				deadline := time.Now().Add(2 * time.Second)
				for {
					_, err := os.Stat(filepath.Dir(socketPath))
					if os.IsNotExist(err) {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("agent socket directory remains after SSH connection closes: %s", socketPath)
					}
					time.Sleep(10 * time.Millisecond)
				}
				if _, err := activeAgent.Sign(publicKey, []byte("after disconnect")); err == nil {
					t.Fatal("agent still signs after the SSH connection closes")
				}
				if err := agentConn.SetDeadline(time.Now().Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				// The accepted socket is idle after signing. It must close fully.
				if _, err := agentConn.Write([]byte{0}); err == nil {
					t.Fatal("accepted agent connection is still writable after disconnect")
				}
			}
		})
	}
}

func checkForwardedAgent(t *testing.T, socketPath string, publicKey gossh.PublicKey) (agent.Agent, net.Conn) {
	t.Helper()
	info, err := os.Stat(filepath.Dir(socketPath))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("expected private agent directory, got mode %o", info.Mode().Perm())
	}
	conn, err := net.DialTimeout("unix", socketPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	forwarded := agent.NewClient(conn)
	keys, err := forwarded.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || !bytes.Equal(keys[0].Blob, publicKey.Marshal()) {
		t.Fatal("forwarded agent returned a different client's key")
	}
	checkAgentSignature(t, forwarded, publicKey)
	return forwarded, conn
}

func checkAgentSignature(t *testing.T, forwarded agent.Agent, publicKey gossh.PublicKey) {
	t.Helper()
	data := []byte("compute agent forwarding test")
	signature, err := forwarded.Sign(publicKey, data)
	if err != nil {
		t.Fatal(err)
	}
	if err := publicKey.Verify(data, signature); err != nil {
		t.Fatalf("forwarded signature is invalid: %v", err)
	}
}
