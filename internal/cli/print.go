/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/duration"
	"sigs.k8s.io/yaml"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
)

// The kinds -o yaml and -o json print. Objects read through a typed clientset come back
// without apiVersion and kind, so the printers set them.
const (
	kindInfraPlane       = "CCLInfraPlane"
	kindInfraPlaneList   = "CCLInfraPlaneList"
	kindComponentSet     = "CCLComponentSet"
	kindComponentSetList = "CCLComponentSetList"
)

// printEncoded writes v as YAML or as indented JSON.
func printEncoded(out io.Writer, v any, output string) error {
	var data []byte
	var err error
	if output == outputJSON {
		data, err = json.MarshalIndent(v, "", "    ")
		data = append(data, '\n')
	} else {
		data, err = yaml.Marshal(v)
	}
	if err != nil {
		return err
	}
	_, err = out.Write(data)
	return err
}

// typeMeta returns the apiVersion and kind of a ccluster.nvidia.com/v1alpha1 kind.
func typeMeta(kind string) metav1.TypeMeta {
	return metav1.TypeMeta{APIVersion: cclusternvidiacomv1alpha1.GroupVersion.String(), Kind: kind}
}

func printInfraPlane(out io.Writer, plane *cclusternvidiacomv1alpha1.CCLInfraPlane, output string) error {
	plane = plane.DeepCopy()
	plane.TypeMeta = typeMeta(kindInfraPlane)
	plane.ManagedFields = nil
	return printEncoded(out, plane, output)
}

func printInfraPlaneList(out io.Writer, list *cclusternvidiacomv1alpha1.CCLInfraPlaneList, output string) error {
	list = list.DeepCopy()
	list.TypeMeta = typeMeta(kindInfraPlaneList)
	if list.Items == nil {
		list.Items = []cclusternvidiacomv1alpha1.CCLInfraPlane{}
	}
	for i := range list.Items {
		list.Items[i].TypeMeta = typeMeta(kindInfraPlane)
		list.Items[i].ManagedFields = nil
	}
	return printEncoded(out, list, output)
}

func printComponentSet(out io.Writer, set *cclusternvidiacomv1alpha1.CCLComponentSet, output string) error {
	set = set.DeepCopy()
	set.TypeMeta = typeMeta(kindComponentSet)
	set.ManagedFields = nil
	return printEncoded(out, set, output)
}

func printComponentSetList(out io.Writer, list *cclusternvidiacomv1alpha1.CCLComponentSetList, output string) error {
	list = list.DeepCopy()
	list.TypeMeta = typeMeta(kindComponentSetList)
	if list.Items == nil {
		list.Items = []cclusternvidiacomv1alpha1.CCLComponentSet{}
	}
	for i := range list.Items {
		list.Items[i].TypeMeta = typeMeta(kindComponentSet)
		list.Items[i].ManagedFields = nil
	}
	return printEncoded(out, list, output)
}

// printInfraPlaneTable prints the columns of kubectl get cclinfraplanes, the CRD's
// printcolumns, after a NAMESPACE column when withNamespace is set.
func printInfraPlaneTable(out io.Writer, planes []cclusternvidiacomv1alpha1.CCLInfraPlane, withNamespace bool) error {
	w := newTable(out)
	_, _ = fmt.Fprintln(w, namespaceCell("NAMESPACE", withNamespace)+"NAME\tDOMAIN\tAVAILABLE\tCLUSTERCONFIG\tVERIFIED\tDNS\tAGE")
	for _, plane := range planes {
		_, _ = fmt.Fprint(w, namespaceCell(plane.Namespace, withNamespace))
		verified := ""
		if plane.Status.ClusterConfig != nil {
			verified = conditionStatus(plane.Status.ClusterConfig.Conditions, cclusternvidiacomv1alpha1.CCLClusterConfigConditionVerified)
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			plane.Name,
			plane.Status.Domain,
			conditionStatus(plane.Status.Conditions, cclusternvidiacomv1alpha1.CCLInfraPlaneConditionAvailable),
			conditionStatus(plane.Status.Conditions, cclusternvidiacomv1alpha1.CCLInfraPlaneConditionClusterConfigApplied),
			verified,
			conditionStatus(plane.Status.Conditions, cclusternvidiacomv1alpha1.CCLInfraPlaneConditionDNSReady),
			age(plane.CreationTimestamp))
	}
	return w.Flush()
}

// printComponentSetTable prints the columns of kubectl get cclcomponentsets, the CRD's
// printcolumns, after a NAMESPACE column when withNamespace is set.
func printComponentSetTable(out io.Writer, sets []cclusternvidiacomv1alpha1.CCLComponentSet, withNamespace bool) error {
	w := newTable(out)
	_, _ = fmt.Fprintln(w, namespaceCell("NAMESPACE", withNamespace)+"NAME\tAPPLIED\tPROGRESS\tAGE")
	for _, set := range sets {
		_, _ = fmt.Fprint(w, namespaceCell(set.Namespace, withNamespace))
		progress := ""
		if c := meta.FindStatusCondition(set.Status.Conditions,
			cclusternvidiacomv1alpha1.CCLComponentSetConditionProgressing); c != nil {
			progress = c.Reason
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			set.Name,
			conditionStatus(set.Status.Conditions, cclusternvidiacomv1alpha1.CCLComponentSetConditionComponentsApplied),
			progress,
			age(set.CreationTimestamp))
	}
	return w.Flush()
}

// namespaceCell returns the cell that starts a row listed across namespaces, or nothing.
func namespaceCell(namespace string, withNamespace bool) string {
	if !withNamespace {
		return ""
	}
	return namespace + "\t"
}

func describeInfraPlane(out io.Writer, plane *cclusternvidiacomv1alpha1.CCLInfraPlane) error {
	w := newTable(out)
	_, _ = fmt.Fprintf(w, "Name:\t%s\n", plane.Name)
	_, _ = fmt.Fprintf(w, "Namespace:\t%s\n", plane.Namespace)
	_, _ = fmt.Fprintf(w, "Domain:\t%s\n", orDash(plane.Status.Domain))
	_, _ = fmt.Fprintf(w, "Credentials:\t%s\n", plane.Spec.ClusterCredentialsRef.Name)
	_, _ = fmt.Fprintf(w, "Created:\t%s\n", created(plane.CreationTimestamp))
	if err := w.Flush(); err != nil {
		return err
	}

	if err := printConditions(out, "Conditions:", "  ", plane.Status.Conditions, plane.Generation); err != nil {
		return err
	}

	clusterConfig := plane.Status.ClusterConfig
	if clusterConfig == nil {
		return nil
	}
	// These conditions are the infra-plane operator's, about the CCLClusterConfig, whose
	// generation is not copied here, so none is marked stale.
	if err := printConditions(out, "Cluster:", "  ", clusterConfig.Conditions, 0); err != nil {
		return err
	}
	if len(clusterConfig.LoadBalancers) == 0 {
		return nil
	}
	_, _ = fmt.Fprintln(out, "  Load balancers:")
	w = newTable(out)
	_, _ = fmt.Fprintln(w, "    NAME\tADDRESS")
	for _, lb := range clusterConfig.LoadBalancers {
		address := lb.IP
		if address == "" {
			address = lb.Hostname
		}
		if address == "" {
			// The Service has no address yet; kubectl shows the same.
			address = "<pending>"
		}
		_, _ = fmt.Fprintf(w, "    %s\t%s\n", lb.Name, address)
	}
	return w.Flush()
}

func describeComponentSet(out io.Writer, set *cclusternvidiacomv1alpha1.CCLComponentSet) error {
	w := newTable(out)
	_, _ = fmt.Fprintf(w, "Name:\t%s\n", set.Name)
	_, _ = fmt.Fprintf(w, "Namespace:\t%s\n", set.Namespace)
	_, _ = fmt.Fprintf(w, "Generation:\t%d (observed %d)\n", set.Generation, set.Status.ObservedGeneration)
	_, _ = fmt.Fprintf(w, "Created:\t%s\n", created(set.CreationTimestamp))
	if err := w.Flush(); err != nil {
		return err
	}

	if err := printConditions(out, "Conditions:", "  ", set.Status.Conditions, set.Generation); err != nil {
		return err
	}

	_, _ = fmt.Fprintln(out, "\nComponents:")
	syncs := map[string]cclusternvidiacomv1alpha1.ComponentSyncStatus{}
	for _, sync := range set.Status.ComponentSyncStatuses {
		syncs[sync.Name] = sync
	}
	healths := map[string]cclusternvidiacomv1alpha1.ComponentHealth{}
	for _, health := range set.Status.ComponentHealthStatuses {
		healths[health.Name] = health.Health
	}
	w = newTable(out)
	_, _ = fmt.Fprintln(w, "  #\tNAME\tCHART\tINFRAPLANE\tNAMESPACE\tSYNC\tHEALTH\tMESSAGE")
	for i, component := range set.Spec.Components {
		syncStatus, message := "-", ""
		if sync, ok := syncs[component.Name]; ok {
			syncStatus, message = orDash(sync.Sync), oneLine(sync.Message)
			// The sync status describes the spec at status.observedGeneration, as Argo CD
			// last compared it. The row shows the spec as it now stands, so the two
			// differ until the operator has written the current spec and Argo CD has
			// compared against it. Health is left unmarked: it describes the objects
			// running now, whichever spec they came from.
			if set.Status.ObservedGeneration < set.Generation || !sync.ComparedToMatchesSpec {
				syncStatus += " (stale)"
			}
		}
		health := orDash(string(healths[component.Name]))
		_, _ = fmt.Fprintf(w, "  %d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			i+1, component.Name, component.Chart, component.InfraPlaneRef.Name, component.Namespace,
			syncStatus, health, message)
	}
	return w.Flush()
}

// printConditions prints a titled table of conditions, after a blank line. A condition
// whose observedGeneration is below generation is marked stale; pass 0 to mark none.
// Nothing is printed for no conditions.
func printConditions(out io.Writer, title, indent string, conditions []metav1.Condition, generation int64) error {
	if len(conditions) == 0 {
		return nil
	}
	_, _ = fmt.Fprintf(out, "\n%s\n", title)
	w := newTable(out)
	_, _ = fmt.Fprintf(w, "%sTYPE\tSTATUS\tREASON\tMESSAGE\n", indent)
	for _, c := range conditions {
		status := string(c.Status)
		if c.ObservedGeneration < generation {
			status += " (stale)"
		}
		_, _ = fmt.Fprintf(w, "%s%s\t%s\t%s\t%s\n", indent, c.Type, status, c.Reason, oneLine(c.Message))
	}
	return w.Flush()
}

func newTable(out io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
}

// conditionStatus returns the status of the condition of type conditionType, or "" if
// there is none, as kubectl prints a missing printcolumn.
func conditionStatus(conditions []metav1.Condition, conditionType string) string {
	if c := meta.FindStatusCondition(conditions, conditionType); c != nil {
		return string(c.Status)
	}
	return ""
}

// age is the time since t as kubectl prints it.
func age(t metav1.Time) string {
	if t.IsZero() {
		return "<unknown>"
	}
	return duration.HumanDuration(time.Since(t.Time))
}

func created(t metav1.Time) string {
	if t.IsZero() {
		return "<unknown>"
	}
	return fmt.Sprintf("%s (%s)", t.UTC().Format(time.RFC3339), age(t))
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// oneLine collapses s's whitespace, newlines included, so it fits one table cell.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
