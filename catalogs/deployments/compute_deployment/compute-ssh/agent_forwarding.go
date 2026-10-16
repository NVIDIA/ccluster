/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package main

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"

	gliderssh "github.com/gliderlabs/ssh"
	gossh "golang.org/x/crypto/ssh"
)

type agentSocketContextKey struct{}

func forwardedAgentSocket(session gliderssh.Session) (string, error) {
	if !gliderssh.AgentRequested(session) {
		return "", nil
	}

	// Agent forwarding belongs to the SSH connection, including all its sessions.
	ctx := session.Context()
	ctx.Lock()
	defer ctx.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if socket, ok := ctx.Value(agentSocketContextKey{}).(string); ok {
		return socket, nil
	}
	sshConn, ok := ctx.Value(gliderssh.ContextKeyConn).(gossh.Conn)
	if !ok {
		return "", errors.New("SSH connection is unavailable")
	}

	dir, err := os.MkdirTemp("", "compute-agent-")
	if err != nil {
		return "", err
	}
	socket := filepath.Join(dir, "agent.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	ctx.SetValue(agentSocketContextKey{}, socket)
	go serveForwardedAgent(ctx, listener, sshConn)
	return socket, nil
}

func serveForwardedAgent(ctx context.Context, listener *net.UnixListener, sshConn gossh.Conn) {
	var mu sync.Mutex
	active := make(map[*net.UnixConn]struct{})
	var workers sync.WaitGroup
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			conn, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			mu.Lock()
			active[conn] = struct{}{}
			mu.Unlock()
			workers.Go(func() {
				defer func() {
					_ = conn.Close()
					mu.Lock()
					delete(active, conn)
					mu.Unlock()
				}()
				forwardAgentConnection(conn, sshConn)
			})
		}
	}()

	<-ctx.Done()
	_ = listener.Close()
	// Stop accepting before closing clients or waiting for workers.
	<-acceptDone
	mu.Lock()
	for conn := range active {
		_ = conn.Close()
	}
	mu.Unlock()
	workers.Wait()
	_ = os.RemoveAll(filepath.Dir(listener.Addr().String()))
}

func forwardAgentConnection(conn *net.UnixConn, sshConn gossh.Conn) {
	channel, requests, err := sshConn.OpenChannel("auth-agent@openssh.com", nil)
	if err != nil {
		return
	}
	defer func() { _ = channel.Close() }()
	go gossh.DiscardRequests(requests)

	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		_, _ = io.Copy(channel, conn)
		_ = channel.CloseWrite()
	}()
	_, _ = io.Copy(conn, channel)
	_ = conn.CloseWrite()
	<-writeDone
}
