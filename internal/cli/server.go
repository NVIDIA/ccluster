/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/NVIDIA/ccluster/internal/server"
	"github.com/NVIDIA/ccluster/pkg/client/clientset/versioned"
)

// defaultListen is where ccluster server listens unless --listen says otherwise.
const defaultListen = "127.0.0.1:8484"

// shutdownTimeout is how long ccluster server waits for open requests when stopped.
const shutdownTimeout = 5 * time.Second

// openBrowser opens url in the user's browser. Tests replace it.
var openBrowser = func(url string) error {
	var name string
	switch runtime.GOOS {
	case "darwin":
		name = "open"
	case "linux", "freebsd", "openbsd", "netbsd":
		name = "xdg-open"
	default:
		return fmt.Errorf("opening a browser is not supported on %s", runtime.GOOS)
	}
	cmd := exec.Command(name, url)
	if err := cmd.Start(); err != nil {
		return err
	}
	// Reap the opener once it exits; it hands the URL to the browser and returns.
	go func() { _ = cmd.Wait() }()
	return nil
}

type serverOptions struct {
	namespace string
	listen    string
	noBrowser bool
}

func newServerCommand() *cobra.Command {
	var opts serverOptions
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Serve a read-only dashboard of the control plane in the browser.",
		Long: `Serve a read-only dashboard of the control plane in the browser.

Shows the infra planes and component sets of the control plane saved by ccluster init:
a summary and a table of each kind, refreshed every few seconds.
It reads with the kubeconfig context's credentials, so Kubernetes RBAC decides what it
shows, and it changes nothing.

Without --namespace it reads across the cluster, which needs cluster-wide read access to
both kinds, and lets you pick a namespace. With --namespace it reads only that one.

It listens on 127.0.0.1:8484 and opens the dashboard in the browser; --listen picks
another loopback address. Stop it with Ctrl-C.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return runServer(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), opts)
		},
	}
	flags := cmd.Flags()
	flags.StringVarP(&opts.namespace, "namespace", "n", "",
		"Show only this control-plane namespace. By default, every namespace is shown.")
	flags.StringVar(&opts.listen, "listen", defaultListen,
		"Loopback address and port to listen on; port 0 picks a free one.")
	flags.BoolVar(&opts.noBrowser, "no-browser", false, "Do not open the dashboard in a browser.")
	return cmd
}

func runServer(ctx context.Context, out, errOut io.Writer, opts serverOptions) error {
	if err := checkLoopback(opts.listen); err != nil {
		return err
	}
	kubeContext, err := loadControlPlane()
	if err != nil {
		return err
	}
	cfg, err := restConfig(kubeContext)
	if err != nil {
		return err
	}
	clientset, err := versioned.NewForConfig(cfg)
	if err != nil {
		return err
	}

	listener, err := net.Listen("tcp", opts.listen)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", opts.listen, err)
	}
	srv := &http.Server{
		Handler: server.New(server.Options{
			Namespace: opts.namespace,
			Clientset: clientset,
			Context:   kubeContext,
			Version:   Version(),
			Logger:    slog.New(slog.NewTextHandler(errOut, nil)),
		}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	url := "http://" + browserAddress(opts.listen, listener.Addr())
	if opts.namespace != "" {
		_, _ = fmt.Fprintf(out, "Serving the ccluster dashboard of %s, namespace %s, at %s\n",
			kubeContext, opts.namespace, url)
	} else {
		_, _ = fmt.Fprintf(out, "Serving the ccluster dashboard of %s at %s\n", kubeContext, url)
	}
	_, _ = fmt.Fprintln(out, "Press Ctrl-C to stop.")
	if !opts.noBrowser {
		if err := openBrowser(url); err != nil {
			_, _ = fmt.Fprintf(errOut, "Could not open a browser (%v); open %s yourself.\n", err, url)
		}
	}

	served := make(chan error, 1)
	go func() { served <- srv.Serve(listener) }()
	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// checkLoopback returns an error unless listen is a host:port whose host is one of
// server.LoopbackHosts. The dashboard reads with the user's own credentials, so no other
// machine may reach it. Other loopback addresses, such as 127.0.0.2, are refused too:
// the browser would send them as the Host header, which the server does not answer.
func checkLoopback(listen string) error {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("--listen %q is not host:port: %w", listen, err)
	}
	if slices.ContainsFunc(server.LoopbackHosts, func(loopback string) bool {
		return strings.EqualFold(host, loopback)
	}) {
		return nil
	}
	return fmt.Errorf("--listen %q is not a loopback address the dashboard answers on; it "+
		"reads with your credentials, so it listens only on %s", listen,
		strings.Join(server.LoopbackHosts, ", "))
}

// browserAddress is the host:port to open: the host as given to --listen, so the
// browser sends a Host header the server accepts, and the port actually bound.
func browserAddress(listen string, bound net.Addr) string {
	host, _, _ := net.SplitHostPort(listen)
	_, port, _ := net.SplitHostPort(bound.String())
	return net.JoinHostPort(host, port)
}
