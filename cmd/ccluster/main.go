/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// ccluster is the command line for the control-plane operator's API: it registers
// infra-plane clusters with a control plane and removes them again. The commands are in
// internal/cli.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/NVIDIA/ccluster/internal/cli"
)

func main() {
	os.Exit(run())
}

// run executes the command and returns the process's exit code. cobra has already
// printed any error.
func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cli.NewRootCommand().ExecuteContext(ctx); err != nil {
		return 1
	}
	return 0
}
