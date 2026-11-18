/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	"sigs.k8s.io/yaml"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
	"github.com/NVIDIA/ccluster/pkg/cclclient"
	cclusterac "github.com/NVIDIA/ccluster/pkg/client/applyconfiguration/ccluster/v1alpha1"
)

func TestListInfraPlanesPrintsThePrintColumns(t *testing.T) {
	setUpInitialized(t)
	namespace := newNamespaceName()
	client := newTestClient(t, namespace)
	applyInfraPlane(t, client, "edge-1")
	applyInfraPlane(t, client, "edge-2")
	writeInfraPlaneStatus(t, client, "edge-1", 1)
	// Another namespace's plane is not listed.
	applyInfraPlane(t, newTestClient(t, newNamespaceName()), "edge-3")

	for _, spelling := range []string{"infraplanes", "infraplane", "ip"} {
		out, err := run(t, "list", spelling, "-n", namespace)
		if err != nil {
			t.Fatalf("list %s: %v", spelling, err)
		}
		rows := tableRows(out)
		want := [][]string{
			{"NAME", "DOMAIN", "AVAILABLE", "CLUSTERCONFIG", "VERIFIED", "DNS", "AGE"},
			{"edge-1", "edge-1." + namespace + ".example.com", "True", "False", "True", "True"},
			// No status: the domain and condition columns are empty.
			{"edge-2"},
		}
		if len(rows) != len(want) {
			t.Fatalf("list %s printed %q, want %d rows", spelling, out, len(want))
		}
		for i, row := range want {
			if got := rows[i][:min(len(row), len(rows[i]))]; strings.Join(got, " ") != strings.Join(row, " ") {
				t.Errorf("list %s row %d = %q, want it to start %q", spelling, i, rows[i], row)
			}
		}
	}
}

func TestListComponentSetsPrintsThePrintColumns(t *testing.T) {
	setUpInitialized(t)
	namespace := newNamespaceName()
	client := newTestClient(t, namespace)
	applyComponentSet(t, client)
	writeComponentSetStatus(t, client)

	for _, spelling := range []string{"componentsets", "componentset", "cs"} {
		out, err := run(t, "list", spelling, "-n", namespace)
		if err != nil {
			t.Fatalf("list %s: %v", spelling, err)
		}
		rows := tableRows(out)
		if len(rows) != 2 ||
			strings.Join(rows[0], " ") != "NAME APPLIED PROGRESS AGE" ||
			strings.Join(rows[1][:3], " ") != "base True WaveSyncing" {
			t.Errorf("list %s printed %q", spelling, out)
		}
	}
}

func TestListAllNamespacesAddsTheNamespaceColumn(t *testing.T) {
	setUpInitialized(t)
	// The API server is shared with the other tests, so only these namespaces' rows are
	// checked.
	first, second := newNamespaceName(), newNamespaceName()
	for _, namespace := range []string{first, second} {
		client := newTestClient(t, namespace)
		applyInfraPlane(t, client, "edge-1")
		applyComponentSet(t, client)
	}

	for _, test := range []struct {
		kind   string
		header string
		name   string
	}{
		{"ip", "NAMESPACE NAME DOMAIN AVAILABLE CLUSTERCONFIG VERIFIED DNS AGE", "edge-1"},
		{"cs", "NAMESPACE NAME APPLIED PROGRESS AGE", "base"},
	} {
		for _, flag := range []string{"-A", "--all-namespaces"} {
			out, err := run(t, "list", test.kind, flag)
			if err != nil {
				t.Fatal(err)
			}
			rows := tableRows(out)
			if strings.Join(rows[0], " ") != test.header {
				t.Errorf("list %s %s printed header %q, want %q", test.kind, flag, rows[0], test.header)
			}
			found := map[string]bool{}
			for _, row := range rows[1:] {
				if row[1] == test.name {
					found[row[0]] = true
				}
			}
			if !found[first] || !found[second] {
				t.Errorf("list %s %s printed %q, want %s in %s and %s", test.kind, flag, out, test.name, first, second)
			}
		}

		out, err := run(t, "list", test.kind, "-A", "-o", "json")
		if err != nil {
			t.Fatal(err)
		}
		items, _ := decode(t, out, outputJSON)["items"].([]any)
		namespaces := map[string]bool{}
		for _, item := range items {
			namespace, _, _ := unstructured.NestedString(item.(map[string]any), "metadata", "namespace")
			namespaces[namespace] = true
		}
		if !namespaces[first] || !namespaces[second] {
			t.Errorf("list %s -A -o json printed %q, want items in %s and %s", test.kind, out, first, second)
		}
	}
}

func TestListOfNothingSaysSo(t *testing.T) {
	setUpInitialized(t)
	namespace := newNamespaceName()

	for kind, want := range map[string]string{
		"ip": "No CCLInfraPlanes found in namespace " + namespace + "\n",
		"cs": "No CCLComponentSets found in namespace " + namespace + "\n",
	} {
		stdout, stderr, err := runSplit(t, "list", kind, "-n", namespace)
		if err != nil {
			t.Fatal(err)
		}
		if stdout != "" || stderr != want {
			t.Errorf("list %s printed %q and %q to stderr, want only %q to stderr", kind, stdout, stderr, want)
		}
	}

	for kind, want := range map[string]string{"ip": "CCLInfraPlaneList", "cs": "CCLComponentSetList"} {
		out, err := run(t, "list", kind, "-n", namespace, "-o", "yaml")
		if err != nil {
			t.Fatal(err)
		}
		var list map[string]any
		if err := yaml.Unmarshal([]byte(out), &list); err != nil {
			t.Fatal(err)
		}
		if items, ok := list["items"].([]any); list["kind"] != want || !ok || len(items) != 0 {
			t.Errorf("list %s -o yaml printed %q, want an empty %s", kind, out, want)
		}
	}
}

func TestListPrintsTheList(t *testing.T) {
	setUpInitialized(t)
	namespace := newNamespaceName()
	client := newTestClient(t, namespace)
	applyInfraPlane(t, client, "edge-1")
	applyComponentSet(t, client)

	tests := []struct{ kind, listKind, itemKind, output string }{
		{"ip", "CCLInfraPlaneList", "CCLInfraPlane", "json"},
		{"ip", "CCLInfraPlaneList", "CCLInfraPlane", "yaml"},
		{"cs", "CCLComponentSetList", "CCLComponentSet", "json"},
		{"cs", "CCLComponentSetList", "CCLComponentSet", "yaml"},
	}
	for _, test := range tests {
		out, err := run(t, "list", test.kind, "-n", namespace, "-o", test.output)
		if err != nil {
			t.Fatal(err)
		}
		list := decode(t, out, test.output)
		items, _ := list["items"].([]any)
		if list["apiVersion"] != "ccluster.nvidia.com/v1alpha1" || list["kind"] != test.listKind || len(items) != 1 {
			t.Fatalf("list %s -o %s printed %q", test.kind, test.output, out)
		}
		expectPrintedObject(t, items[0].(map[string]any), test.itemKind)
	}
}

func TestGetInfraPlaneDescribesIt(t *testing.T) {
	setUpInitialized(t)
	namespace := newNamespaceName()
	client := newTestClient(t, namespace)
	applyInfraPlane(t, client, "edge-1")
	applyInfraPlane(t, client, "edge-2")
	// Computed from generation 0, so stale.
	writeInfraPlaneStatus(t, client, "edge-1", 0)

	out, err := run(t, "get", "infraplane", "edge-1", "-n", namespace)
	if err != nil {
		t.Fatal(err)
	}
	expectLines(t, out,
		"Name: edge-1",
		"Namespace: "+namespace,
		"Domain: edge-1."+namespace+".example.com",
		"Credentials: edge-1-credentials",
		"Conditions:",
		"TYPE STATUS REASON MESSAGE",
		"Available True (stale) Test Argo CD can reach it",
		"ClusterConfigApplied False (stale) Test first line second line",
		"DNSReady True (stale) Test",
		"Cluster:",
		"Verified True Test",
		"Load balancers:",
		"NAME ADDRESS",
		"ingress 10.0.0.50",
		"api api.example.com",
		"grpc <pending>",
	)

	// Without status, only the header is printed.
	for _, spelling := range []string{"infraplanes", "ip"} {
		out, err = run(t, "get", spelling, "edge-2", "-n", namespace)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "Conditions:") || strings.Contains(out, "Cluster:") ||
			!strings.Contains(out, "Name:         edge-2\nNamespace:") || !strings.Contains(out, "Domain:       -\n") {
			t.Errorf("get %s edge-2 printed\n%s", spelling, out)
		}
	}
}

func TestGetComponentSetDescribesIt(t *testing.T) {
	setUpInitialized(t)
	namespace := newNamespaceName()
	client := newTestClient(t, namespace)
	applyComponentSet(t, client)
	writeComponentSetStatus(t, client)

	for _, spelling := range []string{"componentset", "componentsets", "cs"} {
		out, err := run(t, "get", spelling, "base", "-n", namespace)
		if err != nil {
			t.Fatal(err)
		}
		expectLines(t, out,
			"Name: base",
			"Namespace: "+namespace,
			"Generation: 1 (observed 1)",
			"Validated True Test",
			"ComponentsApplied True (stale) Test",
			"Progressing True WaveSyncing waiting on db",
			"Components:",
			"# NAME CHART INFRAPLANE NAMESPACE SYNC HEALTH MESSAGE",
			"1 keda catalogs/cluster_base/keda edge-1 keda Synced Healthy",
			"2 db catalogs/deployments/db edge-1 db OutOfSync (stale) Progressing waiting for the pod",
			"3 queue catalogs/resources/queue edge-1 queue - -",
		)
	}
}

func TestGetComponentSetMarksSyncsOfAnOlderSpecStale(t *testing.T) {
	setUpInitialized(t)
	namespace := newNamespaceName()
	client := newTestClient(t, namespace)
	applyComponentSet(t, client)
	writeComponentSetStatus(t, client)
	// A new chart for keda makes generation 2, which the status does not describe yet.
	set, err := client.GetComponentSet(t.Context(), "base")
	if err != nil {
		t.Fatal(err)
	}
	components := set.Spec.Components
	components[0].Chart = "catalogs/cluster_base/keda_v2"
	if _, err := client.ApplyComponentSet(t.Context(), "base", components); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, "get", "cs", "base", "-n", namespace)
	if err != nil {
		t.Fatal(err)
	}
	expectLines(t, out,
		"Generation: 2 (observed 1)",
		"1 keda catalogs/cluster_base/keda_v2 edge-1 keda Synced (stale) Healthy",
	)
}

func TestGetPrintsTheObject(t *testing.T) {
	setUpInitialized(t)
	namespace := newNamespaceName()
	client := newTestClient(t, namespace)
	applyInfraPlane(t, client, "edge-1")
	applyComponentSet(t, client)

	tests := []struct{ kind, name, wantKind, output string }{
		{"ip", "edge-1", "CCLInfraPlane", "json"},
		{"ip", "edge-1", "CCLInfraPlane", "yaml"},
		{"cs", "base", "CCLComponentSet", "json"},
		{"cs", "base", "CCLComponentSet", "yaml"},
	}
	for _, test := range tests {
		out, err := run(t, "get", test.kind, test.name, "-n", namespace, "--output", test.output)
		if err != nil {
			t.Fatal(err)
		}
		object := decode(t, out, test.output)
		expectPrintedObject(t, object, test.wantKind)
		if name, _, _ := unstructured.NestedString(object, "metadata", "name"); name != test.name {
			t.Errorf("get %s -o %s printed %q", test.kind, test.output, out)
		}
	}
}

func TestListAndGetReject(t *testing.T) {
	setUpInitialized(t)
	namespace := newNamespaceName()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"an unknown type to list", []string{"list", "clusters", "-n", namespace},
			`unknown resource type "clusters"; use infraplane (ip) or componentset (cs)`},
		{"an unknown type to get", []string{"get", "clusters", "x", "-n", namespace}, `unknown resource type "clusters"`},
		{"an unknown output", []string{"list", "ip", "-n", namespace, "-o", "wide"},
			`unknown output format "wide"; use yaml or json`},
		{"get without a name", []string{"get", "ip", "-n", namespace}, "accepts 2 arg(s), received 1"},
		{"list without a type", []string{"list", "-n", namespace}, "accepts 1 arg(s), received 0"},
		{"list without a namespace", []string{"list", "ip"},
			"at least one of the flags in the group [namespace all-namespaces] is required"},
		{"list with a namespace and all of them", []string{"list", "ip", "-n", namespace, "-A"},
			"none of the others can be"},
		// Not read as every namespace, as the clientsets would.
		{"list with an empty namespace", []string{"list", "ip", "-n", ""}, "namespace must not be empty"},
		{"get with an empty namespace", []string{"get", "cs", "base", "-n", ""}, "namespace must not be empty"},
		{"unregister with an empty namespace", []string{"unregister", "edge-1", "-n", ""},
			"namespace must not be empty"},
		{"get without a namespace", []string{"get", "ip", "edge-1"}, `required flag(s) "namespace" not set`},
		{"a missing infra plane", []string{"get", "ip", "edge-1", "-n", namespace},
			`API request failed (404): cclinfraplanes.ccluster.nvidia.com "edge-1" not found`},
		{"a missing component set", []string{"get", "cs", "base", "-n", namespace},
			`API request failed (404): cclcomponentsets.ccluster.nvidia.com "base" not found`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			expectError(t, test.want, test.args...)
		})
	}

	t.Run("a missing init", func(t *testing.T) {
		setUp(t)
		expectError(t, "run ccluster init first", "list", "ip", "-n", namespace)
		expectError(t, "run ccluster init first", "list", "ip", "-A")
	})
}

func newTestClient(t *testing.T, namespace string) *cclclient.Client {
	t.Helper()

	client := cclclient.New(namespace, kubeClient, clientset)
	if err := client.EnsureNamespace(t.Context()); err != nil {
		t.Fatal(err)
	}
	return client
}

func applyInfraPlane(t *testing.T, client *cclclient.Client, name string) {
	t.Helper()

	if _, err := client.ApplyInfraPlane(t.Context(), cclclient.InfraPlaneOptions{Name: name}); err != nil {
		t.Fatal(err)
	}
}

// writeInfraPlaneStatus writes status as the operators would, its conditions computed from
// generation, with the infra plane's cluster status.
func writeInfraPlaneStatus(t *testing.T, client *cclclient.Client, name string, generation int64) {
	t.Helper()

	status := cclusterac.CCLInfraPlaneStatus().WithObservedGeneration(generation).
		WithDomain(name+"."+client.Namespace()+".example.com").WithConditions(
		condition(cclusternvidiacomv1alpha1.CCLInfraPlaneConditionAvailable, metav1.ConditionTrue,
			"Test", "Argo CD can reach it", generation),
		condition(cclusternvidiacomv1alpha1.CCLInfraPlaneConditionClusterConfigApplied, metav1.ConditionFalse,
			"Test", "first line\nsecond line", generation),
		condition(cclusternvidiacomv1alpha1.CCLInfraPlaneConditionDNSReady, metav1.ConditionTrue,
			"Test", "", generation),
	)
	status.WithClusterConfig(cclusterac.CCLClusterConfigStatus().
		WithConditions(condition(cclusternvidiacomv1alpha1.CCLClusterConfigConditionVerified,
			metav1.ConditionTrue, "Test", "", 1)).
		WithLoadBalancers(
			cclusterac.LoadBalancerStatus().WithName("ingress").WithIP("10.0.0.50"),
			cclusterac.LoadBalancerStatus().WithName("api").WithHostname("api.example.com"),
			cclusterac.LoadBalancerStatus().WithName("grpc"),
		))
	if _, err := clientset.CclusterV1alpha1().CCLInfraPlanes(client.Namespace()).ApplyStatus(t.Context(),
		cclusterac.CCLInfraPlane(name, client.Namespace()).WithStatus(status),
		metav1.ApplyOptions{FieldManager: "operator-test", Force: true}); err != nil {
		t.Fatal(err)
	}
}

func applyComponentSet(t *testing.T, client *cclclient.Client) {
	t.Helper()

	components := make([]cclusternvidiacomv1alpha1.ComponentSpec, 0, 3)
	for _, c := range []struct{ name, chart string }{
		{"keda", "catalogs/cluster_base/keda"},
		{"db", "catalogs/deployments/db"},
		{"queue", "catalogs/resources/queue"},
	} {
		components = append(components, cclusternvidiacomv1alpha1.ComponentSpec{
			Name:           c.name,
			InfraPlaneRef:  cclusternvidiacomv1alpha1.LocalObjectReference{Name: "edge-1"},
			ComponentEntry: cclusternvidiacomv1alpha1.ComponentEntry{Chart: c.chart, Namespace: c.name},
		})
	}
	if _, err := client.ApplyComponentSet(t.Context(), "base", components); err != nil {
		t.Fatal(err)
	}
}

// writeComponentSetStatus writes the status of applyComponentSet's set as the operator
// would mid-rollout of generation 1: keda released and converged, db released and
// syncing with Argo CD not yet comparing against the spec the operator wrote, queue not
// yet released, and ComponentsApplied left from the generation before.
func writeComponentSetStatus(t *testing.T, client *cclclient.Client) {
	t.Helper()
	const generation = 1

	status := cclusterac.CCLComponentSetStatus().WithObservedGeneration(generation).
		WithConditions(
			condition(cclusternvidiacomv1alpha1.CCLComponentSetConditionValidated, metav1.ConditionTrue,
				"Test", "", generation),
			condition(cclusternvidiacomv1alpha1.CCLComponentSetConditionComponentsApplied, metav1.ConditionTrue,
				"Test", "", generation-1),
			condition(cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing, metav1.ConditionTrue,
				cclusternvidiacomv1alpha1.CCLComponentSetReasonWaveSyncing, "waiting on db", generation),
		).
		WithComponentSyncStatuses(
			cclusterac.ComponentSyncStatus().WithName("keda").WithComparedToMatchesSpec(true).
				WithSync("Synced"),
			cclusterac.ComponentSyncStatus().WithName("db").WithComparedToMatchesSpec(false).
				WithSync("OutOfSync").WithMessage("waiting for\nthe pod"),
		).
		WithComponentHealthStatuses(
			cclusterac.ComponentHealthStatus().WithName("keda").
				WithHealth(cclusternvidiacomv1alpha1.ComponentHealthHealthy).WithLastTransitionTime(metav1.Now()),
			cclusterac.ComponentHealthStatus().WithName("db").
				WithHealth(cclusternvidiacomv1alpha1.ComponentHealthProgressing).WithLastTransitionTime(metav1.Now()),
		)
	if _, err := clientset.CclusterV1alpha1().CCLComponentSets(client.Namespace()).ApplyStatus(t.Context(),
		cclusterac.CCLComponentSet("base", client.Namespace()).WithStatus(status),
		metav1.ApplyOptions{FieldManager: "operator-test", Force: true}); err != nil {
		t.Fatal(err)
	}
}

func condition(
	conditionType string,
	status metav1.ConditionStatus,
	reason, message string,
	generation int64,
) *metav1ac.ConditionApplyConfiguration {
	return metav1ac.Condition().WithType(conditionType).WithStatus(status).WithReason(reason).
		WithMessage(message).WithLastTransitionTime(metav1.Now()).WithObservedGeneration(generation)
}

// expectLines fails unless each of want is a line of out, ignoring how much whitespace
// separates and surrounds the words: the columns' widths are tabwriter's.
func expectLines(t *testing.T, out string, want ...string) {
	t.Helper()

	lines := map[string]bool{}
	for line := range strings.Lines(out) {
		lines[strings.Join(strings.Fields(line), " ")] = true
	}
	for _, line := range want {
		if !lines[line] {
			t.Errorf("printed\n%s\nwant a line %q", out, line)
		}
	}
}

// tableRows splits a printed table into its rows' whitespace-separated cells.
func tableRows(out string) [][]string {
	var rows [][]string
	for line := range strings.Lines(out) {
		rows = append(rows, strings.Fields(line))
	}
	return rows
}

func decode(t *testing.T, out, output string) map[string]any {
	t.Helper()

	var object map[string]any
	var err error
	if output == outputJSON {
		err = json.Unmarshal([]byte(out), &object)
	} else {
		err = yaml.Unmarshal([]byte(out), &object)
	}
	if err != nil {
		t.Fatalf("decoding %q as %s: %v", out, output, err)
	}
	return object
}

// expectPrintedObject checks an object -o printed carries its apiVersion and kind and no
// managedFields.
func expectPrintedObject(t *testing.T, object map[string]any, kind string) {
	t.Helper()

	if object["apiVersion"] != "ccluster.nvidia.com/v1alpha1" || object["kind"] != kind {
		t.Errorf("printed apiVersion %v and kind %v, want ccluster.nvidia.com/v1alpha1 %s",
			object["apiVersion"], object["kind"], kind)
	}
	metadata, _ := object["metadata"].(map[string]any)
	if _, ok := metadata["managedFields"]; ok || metadata == nil {
		t.Errorf("printed metadata %v, want it without managedFields", metadata)
	}
}

// runSplit executes ccluster with args and returns what it printed to stdout and to
// stderr.
func runSplit(t *testing.T, args ...string) (string, string, error) {
	t.Helper()

	var stdout, stderr bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetArgs(args)
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	err := cmd.ExecuteContext(t.Context())
	return stdout.String(), stderr.String(), err
}
