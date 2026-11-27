/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package ip

// Plain tests of verify with a short deadline, so `go test -run TestVerify` runs
// without the envtest suite.

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
)

const (
	chartStuck           = "catalogs/cluster_base/stuck"
	chartPasses          = "catalogs/cluster_base/passes"
	chartHonoursDeadline = "catalogs/cluster_base/honours-deadline"
	// testTimeout and testGrace stand in for verifyTimeout and verifyGrace. Together they
	// leave a Verifier that returns at once ample time to be collected under -race.
	testTimeout = 250 * time.Millisecond
	testGrace   = time.Second
)

func TestVerifyReportsVerifierIgnoringDeadlineAsTimedOut(t *testing.T) {
	stuck := newStuckVerifier(t)
	// A passing Verifier on either side of the stuck one: the one before is collected
	// while the stuck call is running, the one after once its deadline has passed.
	r := &CCLClusterConfigReconciler{Verifiers: verifierRegistry(
		registration{chartPasses, passes},
		registration{chartStuck, stuck.Verify},
		registration{chartPasses + "-too", passes},
	)}

	total, failing := verifyWithin(t, context.Background(), r)

	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
	if len(failing) != 1 || failing[0].chart != chartStuck {
		t.Fatalf("failing = %v, want only %s", failing, chartStuck)
	}
	if got, want := failing[0].err.Error(), "timed out after 250ms"; got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}

func TestVerifyGivesUpOnAllStuckVerifiersAtOnce(t *testing.T) {
	first, second := newStuckVerifier(t), newStuckVerifier(t)
	r := &CCLClusterConfigReconciler{Verifiers: verifierRegistry(
		registration{chartStuck, first.Verify},
		registration{chartStuck + "-too", second.Verify},
	)}

	start := time.Now()
	_, failing := verifyWithin(t, context.Background(), r)

	// One deadline plus grace for the pass, not one each.
	if elapsed := time.Since(start); elapsed >= 2*(testTimeout+testGrace) {
		t.Errorf("verify took %s, want less than %s", elapsed, 2*(testTimeout+testGrace))
	}
	if len(failing) != 2 {
		t.Fatalf("failing = %v, want both charts", failing)
	}
	for _, f := range failing {
		if got, want := f.err.Error(), "timed out after 250ms"; got != want {
			t.Errorf("%s error = %q, want %q", f.chart, got, want)
		}
	}
}

func TestVerifyStopsWaitingWhenContextEnds(t *testing.T) {
	stuck := newStuckVerifier(t)
	r := &CCLClusterConfigReconciler{Verifiers: verifierRegistry(registration{chartStuck, stuck.Verify})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, failing := verifyWithin(t, ctx, r)

	if elapsed := time.Since(start); elapsed >= testTimeout {
		t.Errorf("verify took %s after ctx ended, want less than %s", elapsed, testTimeout)
	}
	if len(failing) != 1 || failing[0].err.Error() != context.Canceled.Error() {
		t.Fatalf("failing = %v, want %s with %v", failing, chartStuck, context.Canceled)
	}
}

func TestVerifyKeepsErrorOfVerifierHonouringDeadline(t *testing.T) {
	r := &CCLClusterConfigReconciler{Verifiers: verifierRegistry(registration{chartHonoursDeadline, honoursDeadline})}

	_, failing := verifyWithin(t, context.Background(), r)

	want := "timed out after 250ms: listing Leases: context deadline exceeded"
	if len(failing) != 1 || failing[0].err.Error() != want {
		t.Fatalf("failing = %v, want %s with %q", failing, chartHonoursDeadline, want)
	}
}

func TestVerifyDoesNotCallVerifierAgainWhileEarlierCallRuns(t *testing.T) {
	stuck := newStuckVerifier(t)
	r := &CCLClusterConfigReconciler{Verifiers: verifierRegistry(registration{chartStuck, stuck.Verify})}

	verifyWithin(t, context.Background(), r)
	_, failing := verifyWithin(t, context.Background(), r)

	if calls := stuck.calls.Load(); calls != 1 {
		t.Errorf("Verifier called %d times while its first call was still running, want 1", calls)
	}
	if len(failing) != 1 || failing[0].err.Error() != "still running since an earlier pass" {
		t.Fatalf("failing = %v, want %s still running", failing, chartStuck)
	}

	// Once the abandoned call returns, the next pass calls the Verifier again.
	stuck.unblock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, failing = verifyWithin(t, context.Background(), r); len(failing) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("failing = %v after the abandoned call returned, want none", failing)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if calls := stuck.calls.Load(); calls != 2 {
		t.Errorf("Verifier called %d times, want 2", calls)
	}
}

// stuckVerifier blocks until release is closed, whatever its ctx says, as a check
// blocked in a call that takes no context would.
type stuckVerifier struct {
	release chan struct{}
	calls   atomic.Int32
}

func newStuckVerifier(t *testing.T) *stuckVerifier {
	v := &stuckVerifier{release: make(chan struct{})}
	t.Cleanup(v.unblock)
	return v
}

func (v *stuckVerifier) Verify(context.Context, client.Reader, cclusternvidiacomv1alpha1.CCLClusterConfigSpec) error {
	v.calls.Add(1)
	<-v.release
	return nil
}

func (v *stuckVerifier) unblock() {
	select {
	case <-v.release:
	default:
		close(v.release)
	}
}

func passes(context.Context, client.Reader, cclusternvidiacomv1alpha1.CCLClusterConfigSpec) error {
	return nil
}

// verifyWithin runs one pass and fails the test if it has not returned within a few
// seconds, rather than hanging the test binary.
func verifyWithin(t *testing.T, ctx context.Context, r *CCLClusterConfigReconciler) (int, []failure) {
	t.Helper()
	type outcome struct {
		total   int
		failing []failure
	}
	done := make(chan outcome, 1)
	go func() {
		total, failing := r.verify(ctx, cclusternvidiacomv1alpha1.CCLClusterConfigSpec{}, testTimeout, testGrace)
		done <- outcome{total, failing}
	}()
	select {
	case o := <-done:
		return o.total, o.failing
	case <-time.After(5 * time.Second):
		t.Fatal("verify did not return after its Verifier's deadline")
		return 0, nil
	}
}

// honoursDeadline returns ctx's error, wrapped as a read through the client would, once
// ctx is done.
func honoursDeadline(ctx context.Context, _ client.Reader, _ cclusternvidiacomv1alpha1.CCLClusterConfigSpec) error {
	<-ctx.Done()
	return fmt.Errorf("listing Leases: %w", ctx.Err())
}
