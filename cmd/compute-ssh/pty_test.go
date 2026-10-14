//go:build linux

/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// The PTY tests depend on Linux PTY and process-group behaviour. The launcher only
// runs on Linux; on macOS every test here times out waiting for shell output.

package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	gliderssh "github.com/gliderlabs/ssh"
	gossh "golang.org/x/crypto/ssh"
)

func TestShellExitAndOutput(t *testing.T) {
	for _, shell := range []string{"/bin/bash", "/bin/sh"} {
		t.Run(strings.TrimPrefix(shell, "/bin/"), func(t *testing.T) {
			for _, tc := range []struct {
				name       string
				pty        bool
				command    bool
				closeInput bool
				exitCode   int
				outputSize int
			}{
				{name: "pty success", pty: true, outputSize: 128},
				{name: "pty failure", pty: true, exitCode: 7, outputSize: 128},
				{name: "pty large output", pty: true, outputSize: 128 * 1024},
				{name: "pty input EOF", pty: true, closeInput: true, outputSize: 128},
				{name: "command success", command: true, outputSize: 128},
				{name: "command failure", command: true, exitCode: 7, outputSize: 128},
				{name: "shell without pty", outputSize: 128},
			} {
				t.Run(tc.name, func(t *testing.T) {
					client, finished := newShellTestClient(t, shell)
					session, err := client.NewSession()
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = session.Close() }()
					var stdout, stderr bytes.Buffer
					session.Stdout, session.Stderr = &stdout, &stderr
					stdin, err := session.StdinPipe()
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = stdin.Close() }()
					command := fmt.Sprintf(
						`printf 'OUTPUT_%%s\n' START; head -c %d /dev/zero | tr '\000' x; `+
							`printf '\nOUTPUT_%%s\n' END; exit %d`, tc.outputSize, tc.exitCode)
					if tc.pty {
						if err := session.RequestPty("xterm", 24, 80, gossh.TerminalModes{}); err != nil {
							t.Fatal(err)
						}
					}
					if tc.command {
						err = session.Start(command)
					} else {
						err = session.Shell()
						if err == nil {
							_, err = io.WriteString(stdin, command+"\n")
						}
					}
					if err != nil {
						t.Fatal(err)
					}
					if tc.closeInput {
						if err := stdin.Close(); err != nil {
							t.Fatal(err)
						}
					}
					checkShellExit(t, session.Wait(), tc.exitCode)
					output := strings.ReplaceAll(stdout.String(), "\r\n", "\n")
					// The shell builds these markers, so echoed input cannot pass this check.
					want := "OUTPUT_START\n" + strings.Repeat("x", tc.outputSize) + "\nOUTPUT_END\n"
					if !strings.Contains(output, want) {
						t.Errorf("incomplete output: got %d bytes; want the full %d-byte body", len(output), tc.outputSize)
					}
					waitForShellHandler(t, finished)
				})
			}
		})
	}
}

func TestPTYDisconnect(t *testing.T) {
	for _, closeConnection := range []bool{false, true} {
		t.Run(fmt.Sprintf("close connection=%t", closeConnection), func(t *testing.T) {
			client, finished := newShellTestClient(t, "/bin/bash")
			session, stdin, stdout := startTestPTY(t, client)
			command := `trap '' HUP; printf '\nSHELL_PID=%s\n' "$$"; exec sleep 30`
			if _, err := io.WriteString(stdin, command+"\n"); err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(readPTYValue(t, stdout, "SHELL_PID="))
			if err != nil || pid <= 1 {
				t.Fatalf("invalid shell PID: %d (%v)", pid, err)
			}
			if closeConnection {
				_ = client.Close()
			} else {
				_ = session.Close()
			}
			waitForShellHandler(t, finished)
			waitForProcessExit(t, pid)
			if !closeConnection {
				other, err := client.NewSession()
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = other.Close() }()
				output, err := other.CombinedOutput("printf connected")
				if err != nil || string(output) != "connected" {
					t.Fatalf("SSH connection is no longer usable: output=%q error=%v", output, err)
				}
			}
		})
	}
}

func TestPTYResizeAndSignal(t *testing.T) {
	client, finished := newShellTestClient(t, "/bin/bash")
	session, stdin, stdout := startTestPTY(t, client)
	command := `trap 'exit 7' USR1; printf '\nREADY=%s\n' yes; ` +
		`while [ "$(stty size)" != '33 101' ]; do sleep 0.01; done; ` +
		`printf '\nRESIZED=%s\n' yes; while :; do read line; done`
	if _, err := io.WriteString(stdin, command+"\n"); err != nil {
		t.Fatal(err)
	}
	if got := readPTYValue(t, stdout, "READY="); got != "yes" {
		t.Fatalf("unexpected readiness marker: %q", got)
	}
	if err := session.WindowChange(33, 101); err != nil {
		t.Fatal(err)
	}
	if got := readPTYValue(t, stdout, "RESIZED="); got != "yes" {
		t.Fatalf("unexpected resize marker: %q", got)
	}
	resizeDone := make(chan struct{})
	go func() {
		defer close(resizeDone)
		for i := range 100 {
			if session.WindowChange(24+i, 80) != nil {
				return
			}
		}
	}()
	if err := session.Signal(gossh.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, stdout); err != nil {
		t.Fatal(err)
	}
	checkShellExit(t, session.Wait(), 7)
	<-resizeDone
	waitForShellHandler(t, finished)
}

func TestPTYForegroundOutputWithSlowReader(t *testing.T) {
	// Exceed the SSH window while the final bytes still fit in the PTY and copy buffers.
	const outputSize = 2*1024*1024 + 8192
	client, finished := newShellTestClient(t, "/bin/bash")
	session, stdin, stdout := startTestPTY(t, client)
	command := fmt.Sprintf(
		`printf '\nSHELL_PID=%%s\n' "$$"; head -c %d /dev/zero; `+
			`printf '\nFINAL_OUTPUT=%%s\n' complete; exit 7`, outputSize)
	if _, err := io.WriteString(stdin, command+"\n"); err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(readPTYValue(t, stdout, "SHELL_PID="))
	if err != nil || pid <= 1 {
		t.Fatalf("invalid shell PID: %d (%v)", pid, err)
	}
	waitForProcessExit(t, pid)
	// Keep output paused after the shell exits to test a slow client.
	time.Sleep(1500 * time.Millisecond)
	output, err := io.ReadAll(stdout)
	if err != nil {
		t.Fatal(err)
	}
	checkShellExit(t, session.Wait(), 7)
	if got := bytes.Count(output, []byte{0}); got != outputSize {
		t.Errorf("incomplete foreground output: got %d bytes, want %d", got, outputSize)
	}
	if !bytes.Contains(output, []byte("FINAL_OUTPUT=complete")) {
		t.Error("missing final foreground output")
	}
	waitForShellHandler(t, finished)
}

func TestPTYStatusBeforeBackgroundExit(t *testing.T) {
	for _, exitCode := range []uint32{0, 7} {
		t.Run(fmt.Sprintf("exit %d", exitCode), func(t *testing.T) {
			client, finished := newShellTestClient(t, "/bin/bash")
			channel, requests := startTestPTYChannel(t, client)
			stdout := bufio.NewReader(channel)
			command := fmt.Sprintf(
				`trap '' HUP; sleep 30 & printf '\nCHILD_PID=%%s\n' "$!"; exit %d`, exitCode)
			if _, err := io.WriteString(channel, command+"\n"); err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(readPTYValue(t, stdout, "CHILD_PID="))
			if err != nil || pid <= 1 {
				t.Fatalf("invalid child PID: %d (%v)", pid, err)
			}
			defer func() { _ = syscall.Kill(pid, syscall.SIGKILL) }()
			readPTYExitStatus(t, requests, exitCode)
			select {
			case request, ok := <-requests:
				t.Fatalf("channel closed or sent another request while the child held the PTY: open=%t request=%v", ok, request)
			case <-time.After(1500 * time.Millisecond):
			}
			if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			finishTestPTYChannel(t, stdout, requests)
			waitForShellHandler(t, finished)
		})
	}
}

func TestPTYStatusWithBlockedOutput(t *testing.T) {
	const outputSize = 3 * 1024 * 1024
	for _, tc := range []struct {
		name         string
		exitCode     uint32
		closeChannel bool
	}{
		{name: "success"},
		{name: "failure", exitCode: 7},
		{name: "channel closed", exitCode: 7, closeChannel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, finished := newShellTestClient(t, "/bin/bash")
			channel, requests := startTestPTYChannel(t, client)
			stdout := bufio.NewReader(channel)
			// Redirect Bash's logout message so a full PTY cannot block the shell's exit.
			command := fmt.Sprintf(
				`stty -echo; trap '' HUP; printf '\nSHELL_PID=%%s\n' "$$"; `+
					`(printf '\nCHILD_PID=%%s\n' "$BASHPID"; head -c %d /dev/zero; `+
					`printf '\nFINAL_OUTPUT=%%s\n' complete) & exec >/dev/null 2>&1; `+
					`read -r release; exit %d`, outputSize, tc.exitCode)
			if _, err := io.WriteString(channel, command+"\n"); err != nil {
				t.Fatal(err)
			}
			shellPID, err := strconv.Atoi(readPTYValue(t, stdout, "SHELL_PID="))
			if err != nil || shellPID <= 1 {
				t.Fatalf("invalid shell PID: %d (%v)", shellPID, err)
			}
			pid, err := strconv.Atoi(readPTYValue(t, stdout, "CHILD_PID="))
			if err != nil || pid <= 1 {
				t.Fatalf("invalid child PID: %d (%v)", pid, err)
			}
			defer func() { _ = syscall.Kill(pid, syscall.SIGKILL) }()
			// Fill the 2 MiB SSH receive window before allowing the shell to exit.
			time.Sleep(1500 * time.Millisecond)
			if _, err := io.WriteString(channel, "exit\n"); err != nil {
				t.Fatal(err)
			}
			waitForProcessExit(t, shellPID)
			readPTYExitStatus(t, requests, tc.exitCode)
			select {
			case <-finished:
				t.Fatal("handler stopped before the remaining output was read")
			default:
			}
			if tc.closeChannel {
				_ = channel.Close()
				waitForShellHandler(t, finished)
				other, err := client.NewSession()
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = other.Close() }()
				output, err := other.CombinedOutput("printf connected")
				if err != nil || string(output) != "connected" {
					t.Fatalf("SSH connection is no longer usable: output=%q error=%v", output, err)
				}
				return
			}
			output := finishTestPTYChannel(t, stdout, requests)
			if got := bytes.Count(output, []byte{0}); got != outputSize {
				t.Errorf("incomplete background output: got %d bytes, want %d", got, outputSize)
			}
			if !bytes.Contains(output, []byte("FINAL_OUTPUT=complete")) {
				t.Error("missing final background output")
			}
			waitForShellHandler(t, finished)
		})
	}
}

func newShellTestClient(t *testing.T, shell string) (*gossh.Client, <-chan struct{}) {
	t.Helper()
	loginKey, hostKey := newTestSigner(t), newTestSigner(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := newSSHServer(config{
		workingDir:      t.TempDir(),
		shellCandidates: []shellSpec{{path: shell}},
	}, [][]byte{loginKey.PublicKey().Marshal()}, hostKey)
	finished := make(chan struct{}, 2)
	handler := server.Handler
	server.Handler = func(session gliderssh.Session) {
		handler(session)
		finished <- struct{}{}
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		select {
		case <-serveDone:
		case <-time.After(time.Second):
			t.Error("SSH server did not stop")
		}
	})
	conn, err := net.DialTimeout("tcp", listener.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	sshConn, channels, requests, err := gossh.NewClientConn(conn, listener.Addr().String(), &gossh.ClientConfig{
		User: "root", Auth: []gossh.AuthMethod{gossh.PublicKeys(loginKey)},
		HostKeyCallback: gossh.FixedHostKey(hostKey.PublicKey()),
	})
	if err != nil {
		t.Fatal(err)
	}
	client := gossh.NewClient(sshConn, channels, requests)
	t.Cleanup(func() { _ = client.Close() })
	return client, finished
}

func startTestPTY(t *testing.T, client *gossh.Client) (*gossh.Session, io.WriteCloser, *bufio.Reader) {
	t.Helper()
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	stdin, err := session.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := session.RequestPty("xterm", 24, 80, gossh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	if err := session.Shell(); err != nil {
		t.Fatal(err)
	}
	return session, stdin, bufio.NewReader(stdout)
}

func startTestPTYChannel(t *testing.T, client *gossh.Client) (gossh.Channel, <-chan *gossh.Request) {
	t.Helper()
	channel, requests, err := client.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = channel.Close() })
	ptyPayload := gossh.Marshal(&struct {
		Term                      string
		Cols, Rows, Width, Height uint32
		Modes                     string
	}{Term: "xterm", Cols: 80, Rows: 24, Modes: "\x00"})
	if ok, err := channel.SendRequest("pty-req", true, ptyPayload); err != nil || !ok {
		t.Fatalf("PTY request: accepted=%t error=%v", ok, err)
	}
	if ok, err := channel.SendRequest("shell", true, nil); err != nil || !ok {
		t.Fatalf("shell request: accepted=%t error=%v", ok, err)
	}
	return channel, requests
}

func readPTYExitStatus(t *testing.T, requests <-chan *gossh.Request, want uint32) {
	t.Helper()
	select {
	case request, ok := <-requests:
		if !ok {
			t.Fatal("SSH channel closed without an exit status")
		}
		var status struct{ Status uint32 }
		if request.Type != "exit-status" || request.WantReply ||
			gossh.Unmarshal(request.Payload, &status) != nil || status.Status != want {
			t.Fatalf("expected exit status %d, got request %#v", want, request)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("exit status did not arrive while output was paused")
	}
}

func finishTestPTYChannel(t *testing.T, stdout io.Reader, requests <-chan *gossh.Request) []byte {
	t.Helper()
	var output []byte
	done := make(chan error, 1)
	go func() {
		var err error
		output, err = io.ReadAll(stdout)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("terminal output did not finish")
	}
	select {
	case request, ok := <-requests:
		if ok {
			t.Fatalf("unexpected request after exit status: %#v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("SSH channel did not close after output finished")
	}
	return output
}

func readPTYValue(t *testing.T, stdout *bufio.Reader, prefix string) string {
	t.Helper()
	for {
		line, err := stdout.ReadString('\n')
		if err != nil {
			t.Fatalf("shell did not write %s: %v", prefix, err)
		}
		line = strings.TrimRight(line, "\r\n")
		if after, ok := strings.CutPrefix(line, prefix); ok {
			return after
		}
	}
}

func checkShellExit(t *testing.T, err error, want int) {
	t.Helper()
	if want == 0 {
		if err != nil {
			t.Errorf("expected success, got %T: %v", err, err)
		}
		return
	}
	var exitErr *gossh.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitStatus() != want {
		t.Errorf("expected exit %d, got %T: %v", want, err, err)
	}
}

func waitForShellHandler(t *testing.T, finished <-chan struct{}) {
	t.Helper()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("shell handler did not stop")
	}
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("process %d is still running", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
