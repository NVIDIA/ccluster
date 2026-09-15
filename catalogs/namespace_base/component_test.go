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

package namespacebase_test

import (
	"testing"

	namespacebase "github.com/NVIDIA/ccluster/catalogs/namespace_base"
)

// TestNew: the chart takes no values, so the entry carries none and Helm renders the
// chart against the release namespace alone.
func TestNew(t *testing.T) {
	entry, err := namespacebase.New("user-7", namespacebase.Values{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if entry.Chart != namespacebase.Chart || entry.Namespace != "user-7" {
		t.Errorf("New() = %+v, want chart %q in user-7", entry, namespacebase.Chart)
	}
	if entry.Values != nil {
		t.Errorf("Values = %s, want nil", entry.Values.Raw)
	}
}
