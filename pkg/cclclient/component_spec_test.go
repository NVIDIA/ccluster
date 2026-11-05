/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package cclclient

import (
	"encoding/json"
	"reflect"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
)

// TestComponentSpecCopiesEveryField guards componentSpec, which copies ComponentSpec into
// its apply configuration field by field. Every field of a ComponentSpec is set to a
// distinct value, and the apply configuration must serialise to the same JSON. A field
// added to ComponentSpec, or to the ComponentEntry it embeds, fails here until
// componentSpec copies it too.
func TestComponentSpecCopiesEveryField(t *testing.T) {
	var component cclusternvidiacomv1alpha1.ComponentSpec
	fillEveryField(t, reflect.ValueOf(&component).Elem(), "component")

	want := toJSONMap(t, component)
	got := toJSONMap(t, componentSpec(component))
	if !reflect.DeepEqual(got, want) {
		t.Errorf("componentSpec does not copy every field of ComponentSpec; update it "+
			"for the API change.\n got: %v\nwant: %v", got, want)
	}
}

// fillEveryField sets every field reachable from v to a non-zero value derived from its
// path, so a field copied into the wrong place shows up as well as one not copied.
func fillEveryField(t *testing.T, v reflect.Value, path string) {
	t.Helper()
	if v.Type() == reflect.TypeFor[apiextensionsv1.JSON]() {
		v.Set(reflect.ValueOf(apiextensionsv1.JSON{Raw: []byte(`{"path":"` + path + `"}`)}))
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(path)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fillEveryField(t, v.Elem(), path)
	case reflect.Struct:
		for i := range v.NumField() {
			if field := v.Type().Field(i); field.IsExported() {
				fillEveryField(t, v.Field(i), path+"."+field.Name)
			}
		}
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fillEveryField(t, v.Index(0), path+"[0]")
	default:
		t.Fatalf("%s: fillEveryField cannot fill a %s; extend it", path, v.Type())
	}
}

func toJSONMap(t *testing.T, v any) map[string]any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshalling %T: %v", v, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshalling %T: %v", v, err)
	}
	return m
}
