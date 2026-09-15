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

package persistentvolume_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/NVIDIA/ccluster/catalogs/deployments"
	persistentvolume "github.com/NVIDIA/ccluster/catalogs/resources/persistent_volume"
)

func TestValidate(t *testing.T) {
	fs := &persistentvolume.Filesystem{SizeGB: 100, StorageClassName: "fast"}
	obj := &persistentvolume.Object{Bucket: "models", Region: "us-east-1"}
	tests := []struct {
		name    string
		values  persistentvolume.Values
		wantErr error
		wantMsg string
	}{
		{name: "filesystem", values: persistentvolume.Values{VolumeID: "1", Name: "home", Filesystem: fs}},
		{name: "object", values: persistentvolume.Values{VolumeID: "2", Name: "models", Object: obj}},
		{
			name:    "both backends",
			values:  persistentvolume.Values{VolumeID: "3", Name: "x", Filesystem: fs, Object: obj},
			wantErr: deployments.ErrInvalidValue, wantMsg: "mutually exclusive",
		},
		{
			name:    "no backend",
			values:  persistentvolume.Values{VolumeID: "3", Name: "x"},
			wantErr: deployments.ErrMissingValue, wantMsg: "one of filesystem or object",
		},
		{
			name:    "missing identifiers",
			values:  persistentvolume.Values{Filesystem: fs},
			wantErr: deployments.ErrMissingValue, wantMsg: "volumeId, name",
		},
		{
			name: "zero size",
			values: persistentvolume.Values{
				VolumeID: "1", Name: "x", Filesystem: &persistentvolume.Filesystem{StorageClassName: "fast"},
			},
			wantErr: deployments.ErrInvalidValue, wantMsg: "sizeGb",
		},
		{
			name:    "bucket without region",
			values:  persistentvolume.Values{VolumeID: "1", Name: "x", Object: &persistentvolume.Object{Bucket: "b"}},
			wantErr: deployments.ErrMissingValue, wantMsg: "object.region",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := persistentvolume.New("user-7", test.values)
			if test.wantErr == nil {
				if err != nil {
					t.Fatalf("New() error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, test.wantErr) || !strings.Contains(err.Error(), test.wantMsg) {
				t.Fatalf("New() error = %v, want one wrapping %v mentioning %q", err, test.wantErr, test.wantMsg)
			}
		})
	}
}
