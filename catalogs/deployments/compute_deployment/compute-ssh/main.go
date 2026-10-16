/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// compute-ssh is the SSH server of catalogs/deployments/compute_deployment. The
// chart's init container runs `compute-ssh install <path>` to copy the binary into a
// shared volume, and the user container then runs it as its entrypoint.
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
	gliderssh "github.com/gliderlabs/ssh"
	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

const (
	defaultAddr              = ":2222"
	defaultAuthorizedKeyFile = "/authorized_keys"
	defaultHostKeyDir        = "/home/.ccluster/ssh"
	defaultWorkingDir        = "/home"
	sshUser                  = "root"
	sshHome                  = "/root"
)

type config struct {
	addr              string
	authorizedKeyFile string
	hostKeyDir        string
	workingDir        string
	shellCandidates   []shellSpec
}

type shellSpec struct {
	path string
	name string
}

func main() {
	if len(os.Args) == 3 && os.Args[1] == "install" {
		if err := installSelf(os.Args[2]); err != nil {
			log.Fatalf("failed to install compute SSH launcher: %v", err)
		}
		return
	}

	cfg := loadConfig()

	authorizedKeys, err := loadAuthorizedKeys(cfg.authorizedKeyFile)
	if err != nil {
		log.Fatalf("failed to load authorized keys: %v", err)
	}
	if len(authorizedKeys) == 0 {
		log.Fatalf("no authorized SSH public keys found in %s", cfg.authorizedKeyFile)
	}

	hostSigner, err := loadOrCreateHostSigner(cfg.hostKeyDir)
	if err != nil {
		log.Fatalf("failed to load or create SSH host key: %v", err)
	}

	server := newSSHServer(cfg, authorizedKeys, hostSigner)

	log.Printf("starting compute SSH launcher on %s", cfg.addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, gliderssh.ErrServerClosed) {
		log.Fatalf("SSH server exited: %v", err)
	}
}

func newSSHServer(cfg config, authorizedKeys [][]byte, hostSigner gossh.Signer) *gliderssh.Server {
	forwardedTCPHandler := &gliderssh.ForwardedTCPHandler{}
	server := &gliderssh.Server{
		Addr:             cfg.addr,
		Handler:          sessionHandler(cfg),
		PublicKeyHandler: publicKeyHandler(authorizedKeys),
		SubsystemHandlers: map[string]gliderssh.SubsystemHandler{
			"sftp": sftpHandler,
		},
		ChannelHandlers: map[string]gliderssh.ChannelHandler{
			"session":      gliderssh.DefaultSessionHandler,
			"direct-tcpip": gliderssh.DirectTCPIPHandler,
		},
		RequestHandlers: map[string]gliderssh.RequestHandler{
			"tcpip-forward":        forwardedTCPHandler.HandleSSHRequest,
			"cancel-tcpip-forward": forwardedTCPHandler.HandleSSHRequest,
		},
		LocalPortForwardingCallback: func(
			ctx gliderssh.Context, destinationHost string, destinationPort uint32,
		) bool {
			log.Printf("accepted local port forward: user=%s destination=%s:%d remote=%s",
				ctx.User(), destinationHost, destinationPort, ctx.RemoteAddr())
			return true
		},
		ReversePortForwardingCallback: func(ctx gliderssh.Context, bindHost string, bindPort uint32) bool {
			log.Printf("accepted remote port forward: user=%s bind=%s:%d remote=%s",
				ctx.User(), bindHost, bindPort, ctx.RemoteAddr())
			return true
		},
	}
	server.AddHostKey(hostSigner)
	return server
}

func installSelf(destination string) (err error) {
	source, err := os.Open("/proc/self/exe")
	if err != nil {
		executable, executableErr := os.Executable()
		if executableErr != nil {
			return err
		}
		source, err = os.Open(executable)
		if err != nil {
			return err
		}
	}
	defer func() { _ = source.Close() }()

	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}

	target, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := target.Close(); err == nil {
			err = closeErr
		}
	}()

	if _, err = io.Copy(target, source); err != nil {
		return err
	}
	return target.Chmod(0o755)
}

func loadConfig() config {
	return config{
		addr:              getEnv("CCLUSTER_SSH_ADDR", defaultAddr),
		authorizedKeyFile: getEnv("SSH_PUBLIC_KEY_FILE", defaultAuthorizedKeyFile),
		hostKeyDir:        getEnv("CCLUSTER_SSH_HOST_KEY_DIR", defaultHostKeyDir),
		workingDir:        getEnv("CCLUSTER_SSH_WORKDIR", defaultWorkingDir),
		shellCandidates:   defaultShellCandidates(),
	}
}

func getEnv(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func loadAuthorizedKeys(path string) ([][]byte, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var keys [][]byte
	for len(bytes.TrimSpace(content)) > 0 {
		pubKey, _, _, rest, err := gossh.ParseAuthorizedKey(content)
		if err != nil {
			return nil, err
		}
		keys = append(keys, pubKey.Marshal())
		content = rest
	}
	return keys, nil
}

func publicKeyHandler(authorizedKeys [][]byte) gliderssh.PublicKeyHandler {
	return func(ctx gliderssh.Context, key gliderssh.PublicKey) bool {
		if ctx.User() != sshUser {
			log.Printf("rejected SSH login for unsupported user=%s remote=%s", ctx.User(), ctx.RemoteAddr())
			return false
		}

		keyBytes := key.Marshal()
		for _, authorizedKey := range authorizedKeys {
			if bytes.Equal(keyBytes, authorizedKey) {
				return true
			}
		}
		log.Printf("rejected SSH public key for user=%s remote=%s", ctx.User(), ctx.RemoteAddr())
		return false
	}
}

func loadOrCreateHostSigner(hostKeyDir string) (gossh.Signer, error) {
	if err := os.MkdirAll(hostKeyDir, 0o700); err != nil {
		return nil, err
	}

	keyPath := filepath.Join(hostKeyDir, "ssh_host_ed25519_key")
	keyBytes, err := os.ReadFile(keyPath)
	if err == nil {
		return gossh.ParsePrivateKey(keyBytes)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	privateKeyBytes, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyBytes})
	if pemBytes == nil {
		return nil, errors.New("failed to encode SSH host key")
	}
	if err := os.WriteFile(keyPath, pemBytes, 0o600); err != nil {
		return nil, err
	}
	return gossh.ParsePrivateKey(pemBytes)
}

func sessionHandler(cfg config) gliderssh.Handler {
	return func(session gliderssh.Session) {
		agentSocket, err := forwardedAgentSocket(session)
		if err != nil {
			log.Printf("failed to start SSH agent forwarding: %v", err)
			_, _ = fmt.Fprintln(session.Stderr(), "failed to start SSH agent forwarding")
			_ = session.Exit(1)
			return
		}

		if len(session.Command()) > 0 {
			runRemoteCommand(cfg, session, agentSocket)
			return
		}
		runInteractiveShell(cfg, session, agentSocket)
	}
}

func sftpHandler(session gliderssh.Session) {
	server, err := sftp.NewServer(session)
	if err != nil {
		log.Printf("failed to start SFTP subsystem: %v", err)
		_ = session.Exit(1)
		return
	}
	if err := server.Serve(); err != nil && !errors.Is(err, io.EOF) {
		log.Printf("SFTP subsystem failed: %v", err)
		_ = session.Exit(1)
		return
	}
	_ = server.Close()
}

func runInteractiveShell(cfg config, session gliderssh.Session, agentSocket string) {
	shell, err := chooseShell(cfg.shellCandidates)
	if err != nil {
		writeUnsupportedShellMessage(session, err)
		_ = session.Exit(127)
		return
	}

	ptyReq, windowChanges, hasPTY := session.Pty()
	cmd := exec.Command(shell.path, "-l")
	cmd.Dir = existingDirOrDefault(cfg.workingDir)

	if !hasPTY {
		cmd.Env = buildEnv(session, shell, agentSocket, os.Environ()...)
		runNoPTY(session.Context(), session, cmd)
		return
	}

	cmd.Env = buildEnv(session, shell, agentSocket, append(os.Environ(), "TERM="+ptyReq.Term)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setctty: true, Setsid: true}
	ptyFile, err := pty.StartWithSize(cmd, &pty.Winsize{
		Rows: uint16(ptyReq.Window.Height),
		Cols: uint16(ptyReq.Window.Width),
	})
	if err != nil {
		log.Printf("failed to start PTY shell %s: %v", shell.path, err)
		_, _ = fmt.Fprintf(session.Stderr(), "failed to start shell: %v\n", err)
		_ = session.Exit(1)
		return
	}
	defer func() { _ = ptyFile.Close() }()

	// Nonblocking I/O lets Close stop a read when the client disconnects.
	if err := syscall.SetNonblock(int(ptyFile.Fd()), true); err != nil {
		log.Printf("failed to configure PTY: %v", err)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = session.Exit(1)
		return
	}

	ctx, cancel := context.WithCancel(session.Context())
	defer cancel()
	stopClose := context.AfterFunc(ctx, func() { _ = ptyFile.Close() })
	defer stopClose()
	go func() {
		forwardPTYRequests(session, ptyFile, windowChanges, cmd.Process)
		cancel()
	}()
	go func() { _, _ = io.Copy(ptyFile, session) }()
	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		_, _ = io.Copy(session, ptyFile)
	}()

	exitCode := waitForCommand(ctx, cmd, ptyFile)
	// Close before return to prevent the library's fallback exit status.
	defer func() { _ = session.Close() }()

	// Report exit now, then let remaining output finish under SSH flow control.
	status := struct{ Status uint32 }{uint32(exitCode)}
	if _, err := session.SendRequest("exit-status", false, gossh.Marshal(&status)); err != nil {
		return
	}
	<-outputDone
}

func runRemoteCommand(cfg config, session gliderssh.Session, agentSocket string) {
	shell, err := chooseShell(cfg.shellCandidates)
	if err != nil {
		writeUnsupportedShellMessage(session, err)
		_ = session.Exit(127)
		return
	}

	rawCommand := session.RawCommand()
	cmd := newRemoteCommand(shell, rawCommand)
	cmd.Dir = existingDirOrDefault(cfg.workingDir)
	cmd.Env = buildEnv(session, shell, agentSocket, os.Environ()...)

	exitCode := runCommandWithSessionIO(session.Context(), session, cmd)
	_ = session.Exit(exitCode)
}

func newRemoteCommand(shell shellSpec, rawCommand string) *exec.Cmd {
	return exec.Command(shell.path, "-c", rawCommand)
}

func defaultShellCandidates() []shellSpec {
	return []shellSpec{
		{path: "/bin/bash", name: "bash"},
		{path: "/usr/bin/bash", name: "bash"},
		{path: "/bin/sh", name: "sh"},
		{path: "/usr/bin/sh", name: "sh"},
	}
}

func chooseShell(candidates []shellSpec) (shellSpec, error) {
	return chooseShellFrom(candidates)
}

func chooseShellFrom(candidates []shellSpec) (shellSpec, error) {
	for _, candidate := range candidates {
		info, err := os.Stat(candidate.path)
		if err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return shellSpec{}, errors.New("no supported shell found; expected /bin/bash or /bin/sh")
}

func writeUnsupportedShellMessage(writer io.Writer, err error) {
	_, _ = fmt.Fprintf(writer, "unsupported image for SSH shell access: %v\n", err)
}

func existingDirOrDefault(path string) string {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return path
	}
	return "/"
}

func buildEnv(session gliderssh.Session, shell shellSpec, agentSocket string, baseEnv ...string) []string {
	env := append([]string{}, baseEnv...)
	for _, requested := range session.Environ() {
		if strings.Contains(requested, "=") {
			env = append(env, requested)
		}
	}
	if agentSocket != "" {
		env = setEnv(env, "SSH_AUTH_SOCK", agentSocket)
	}
	env = setEnv(env, "SHELL", shell.path)
	env = setEnv(env, "HOME", sshHome)
	env = setEnv(env, "USER", sshUser)
	return setEnv(env, "LOGNAME", sshUser)
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

func runNoPTY(ctx context.Context, session gliderssh.Session, cmd *exec.Cmd) {
	exitCode := runCommandWithSessionIO(ctx, session, cmd)
	_ = session.Exit(exitCode)
}

// runCommandWithSessionIO wires the SSH session to a non-PTY command's stdio and
// runs it to completion.
//
// It deliberately does NOT assign the session directly to cmd.Stdin. Doing so
// makes Go's os/exec couple process exit to stdin EOF (cmd.Wait blocks until the
// stdin source returns EOF), which deadlocks bidirectional protocols such as
// rsync and git-over-ssh: those clients withhold stdin EOF until they receive
// the command's exit status, but we only send the exit status after Wait
// returns. Using a StdinPipe with a detached copy goroutine lets Wait return as
// soon as the process exits; the exit status is then delivered and the client
// closes stdin, which unblocks the copy.
func runCommandWithSessionIO(ctx context.Context, session gliderssh.Session, cmd *exec.Cmd) int {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		log.Printf("failed to open stdin pipe: %v", err)
		return 1
	}
	cmd.Stdout = session
	cmd.Stderr = session.Stderr()

	if err := cmd.Start(); err != nil {
		log.Printf("failed to start command %q: %v", strings.Join(cmd.Args, " "), err)
		_ = stdin.Close()
		return 1
	}

	go func() {
		_, _ = io.Copy(stdin, session)
		_ = stdin.Close()
	}()

	exitCode := waitForCommand(ctx, cmd, nil)
	_ = stdin.Close()
	return exitCode
}

func waitForCommand(ctx context.Context, cmd *exec.Cmd, ptyFile *os.File) int {
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case <-ctx.Done():
		if cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGHUP)
			time.Sleep(250 * time.Millisecond)
			_ = cmd.Process.Kill()
		}
		return 255
	case err := <-done:
		if err == nil {
			return 0
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		if ptyFile != nil && errors.Is(err, os.ErrClosed) {
			return 0
		}
		log.Printf("command failed: %v", err)
		return 1
	}
}

func forwardPTYRequests(
	session gliderssh.Session, ptyFile *os.File, windowChanges <-chan gliderssh.Window, process *os.Process,
) {
	terminal, err := ptyFile.SyscallConn()
	if err != nil {
		return
	}

	signals := make(chan gliderssh.Signal, 1)
	session.Signals(signals)
	for {
		select {
		case win, ok := <-windowChanges:
			if !ok {
				return
			}
			// Control keeps the descriptor open and preserves nonblocking I/O.
			_ = terminal.Control(func(fd uintptr) {
				_ = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{
					Row: uint16(win.Height),
					Col: uint16(win.Width),
				})
			})
		case signal := <-signals:
			if sysSignal, ok := mapSignal(signal); ok {
				_ = process.Signal(sysSignal)
			}
		}
	}
}

func mapSignal(signal gliderssh.Signal) (os.Signal, bool) {
	switch signal {
	case gliderssh.SIGABRT:
		return syscall.SIGABRT, true
	case gliderssh.SIGALRM:
		return syscall.SIGALRM, true
	case gliderssh.SIGFPE:
		return syscall.SIGFPE, true
	case gliderssh.SIGHUP:
		return syscall.SIGHUP, true
	case gliderssh.SIGILL:
		return syscall.SIGILL, true
	case gliderssh.SIGINT:
		return syscall.SIGINT, true
	case gliderssh.SIGKILL:
		return syscall.SIGKILL, true
	case gliderssh.SIGPIPE:
		return syscall.SIGPIPE, true
	case gliderssh.SIGQUIT:
		return syscall.SIGQUIT, true
	case gliderssh.SIGSEGV:
		return syscall.SIGSEGV, true
	case gliderssh.SIGTERM:
		return syscall.SIGTERM, true
	case gliderssh.SIGUSR1:
		return syscall.SIGUSR1, true
	case gliderssh.SIGUSR2:
		return syscall.SIGUSR2, true
	default:
		return nil, false
	}
}
