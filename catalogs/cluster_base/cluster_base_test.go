/*
Copyright 2026 NVIDIA CORPORATION & AFFILIATES.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package clusterbase_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/NVIDIA/ccluster/catalogs"
	clusterbase "github.com/NVIDIA/ccluster/catalogs/cluster_base"
	teleportkubeagent "github.com/NVIDIA/ccluster/catalogs/cluster_base/teleport_kube_agent"
)

// dns1123 is the pattern api/v1alpha1.ComponentSpec enforces on both name and
// namespace. Checking it here means a bad catalog entry fails in go test rather than
// at kubectl apply.
var dns1123 = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// repoRoot is two levels up from catalogs/cluster_base, which is this test's working
// directory. Chart paths are relative to it.
const repoRoot = "../.."

// maxComponents is api/v1alpha1.CCLDeploymentSpec's MaxItems.
const maxComponents = 50

func TestComponentsAreValidComponentSpecs(t *testing.T) {
	components := clusterbase.Components()

	if len(components) > maxComponents {
		t.Errorf("len(Components()) = %d, exceeds the CRD's MaxItems of %d",
			len(components), maxComponents)
	}

	seen := make(map[string]bool, len(components))
	for _, component := range components {
		entry := component.Entry()

		// Mirrors the CEL rule "component names must be unique".
		if seen[entry.Name] {
			t.Errorf("duplicate component name %q", entry.Name)
		}
		seen[entry.Name] = true

		if !dns1123.MatchString(entry.Name) || len(entry.Name) > 63 {
			t.Errorf("component name %q is not a valid ComponentSpec.Name", entry.Name)
		}
		if !dns1123.MatchString(entry.Namespace) || len(entry.Namespace) > 63 {
			t.Errorf("component %q namespace %q is not valid", entry.Name, entry.Namespace)
		}
		if len(entry.Chart) > 512 {
			t.Errorf("component %q chart path exceeds the CRD's MaxLength of 512", entry.Name)
		}
	}
}

// TestChartPathsExist catches a typo'd or renamed chart directory here, rather than
// hours later as a ComparisonError from Argo CD's repo-server.
func TestChartPathsExist(t *testing.T) {
	for _, component := range clusterbase.Components() {
		entry := component.Entry()
		chartYAML := filepath.Join(repoRoot, entry.Chart, "Chart.yaml")
		if _, err := os.Stat(chartYAML); err != nil {
			t.Errorf("component %q: %v", entry.Name, err)
		}
	}
}

// TestEveryChartIsInTheCatalog is the reverse direction, and the reason it matters: a
// chart added to catalogs/cluster_base but never added to Components() would be
// silently undeployable, with nothing to notice it.
func TestEveryChartIsInTheCatalog(t *testing.T) {
	inCatalog := make(map[string]bool)
	for _, component := range clusterbase.Components() {
		inCatalog[component.Entry().Chart] = true
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading catalogs/cluster_base: %v", err)
	}

	for _, dir := range entries {
		if !dir.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir.Name(), "Chart.yaml")); err != nil {
			continue
		}
		chart := "catalogs/cluster_base/" + dir.Name()
		if !inCatalog[chart] {
			t.Errorf("chart %q is not in Components(), so it can never be deployed", chart)
		}
	}
}

func TestConfigureFillsTeleportRequiredValues(t *testing.T) {
	// Unconfigured, the catalog must not convert: teleport-kube-agent's four
	// required values ship as placeholders and nothing rejects them at admission.
	if _, err := catalogs.ToComponentSpecs(clusterbase.Components(), "plane-a"); err == nil {
		t.Error("ToComponentSpecs() on an unconfigured catalog = nil error, want one")
	}

	components := clusterbase.Configure(clusterbase.Config{
		Teleport: teleportkubeagent.Config{
			ProxyAddr:           "teleport.example.com:443",
			TeleportClusterName: "prod",
			KubeClusterName:     "ccluster-local",
			TokenName:           "kube-token",
		},
	})

	specs, err := catalogs.ToComponentSpecs(components, "plane-a")
	if err != nil {
		t.Fatalf("ToComponentSpecs() error = %v, want nil", err)
	}
	if len(specs) != len(components) {
		t.Fatalf("len(specs) = %d, want %d", len(specs), len(components))
	}

	// Order is the rollout contract, so assert the ends of it explicitly.
	if specs[0].Name != "spire-crds" {
		t.Errorf("specs[0].Name = %q, want %q", specs[0].Name, "spire-crds")
	}
	last := specs[len(specs)-1]
	if last.Name != "teleport-kube-agent" {
		t.Errorf("last component = %q, want %q", last.Name, "teleport-kube-agent")
	}
	if last.Values == nil {
		t.Error("teleport-kube-agent values = nil, want the configured values")
	}
}
