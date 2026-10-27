/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

// clientschema writes the OpenAPI schema that `make client-generate` passes to
// applyconfiguration-gen. The generator embeds it in pkg/client: the fake clientset needs
// it to track field ownership and fails every write without it, and the generator adds an
// Extract function for each kind it covers.
//
// The schema is built from the CRDs under --crds with the API server's own CRD OpenAPI
// builder, so the fake and a real server agree on which fields are atomic and which are
// merged. The server names a kind's definition after its group, version and kind, and the
// generated client looks it up under the name runtime.Scheme gives the kind's Go type, so
// each kind's definition is renamed to that.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/controller/openapi/builder"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/kube-openapi/pkg/validation/spec"
	"sigs.k8s.io/yaml"

	cclusternvidiacomv1alpha1 "github.com/NVIDIA/ccluster/api/ccluster/v1alpha1"
)

func main() {
	crds := flag.String("crds", "config/crd/bases", "directory holding the CRD manifests")
	out := flag.String("out", "dist/clientschema.json", "file to write the OpenAPI v2 schema to")
	flag.Parse()

	if err := run(*crds, *out); err != nil {
		fmt.Fprintln(os.Stderr, "clientschema:", err)
		os.Exit(1)
	}
}

func run(dir, out string) error {
	scheme := runtime.NewScheme()
	if err := cclusternvidiacomv1alpha1.AddToScheme(scheme); err != nil {
		return err
	}

	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no CRDs under %s; run `make manifests` first", dir)
	}

	definitions := spec.Definitions{}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		var crd apiextensionsv1.CustomResourceDefinition
		if err := yaml.Unmarshal(raw, &crd); err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}

		for _, version := range crd.Spec.Versions {
			if !version.Served {
				continue
			}
			gvk := schema.GroupVersionKind{Group: crd.Spec.Group, Version: version.Name, Kind: crd.Spec.Names.Kind}
			// applyconfiguration-gen reads OpenAPI v2. The v2 conversion drops the anyOf,
			// allOf, oneOf and not junctors, which only validate values, and leaves a
			// nullable field untyped, so the fake accepts any value there.
			swagger, err := builder.BuildOpenAPIV2(&crd, version.Name, builder.Options{V2: true})
			if err != nil {
				return fmt.Errorf("%s: %w", gvk, err)
			}
			name, err := scheme.ToOpenAPIDefinitionName(gvk)
			if err != nil {
				return fmt.Errorf("%s: %w", gvk, err)
			}

			// The server names the group's definitions after the group reversed:
			// ccluster.nvidia.com/v1alpha1 CCLInfraPlane is com.nvidia.ccluster.v1alpha1.CCLInfraPlane.
			groupParts := strings.Split(gvk.Group, ".")
			slices.Reverse(groupParts)
			prefix := strings.Join(groupParts, ".") + "."
			kind, ok := swagger.Definitions[prefix+gvk.Version+"."+gvk.Kind]
			if !ok {
				return fmt.Errorf("%s: the API server's builder named no definition %s",
					gvk, prefix+gvk.Version+"."+gvk.Kind)
			}
			definitions[name] = kind
			for defName, def := range swagger.Definitions {
				// Skip the kind, renamed above, and its List kind, which only the server's
				// list endpoint uses. What is left are the meta/v1 types the kind refers to.
				if !strings.HasPrefix(defName, prefix) {
					definitions[defName] = def
				}
			}
		}
	}

	raw, err := json.MarshalIndent(&spec.Swagger{SwaggerProps: spec.SwaggerProps{
		Swagger:     "2.0",
		Info:        &spec.Info{InfoProps: spec.InfoProps{Title: "ccluster", Version: "unversioned"}},
		Definitions: definitions,
	}}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	return os.WriteFile(out, append(raw, '\n'), 0o644)
}
