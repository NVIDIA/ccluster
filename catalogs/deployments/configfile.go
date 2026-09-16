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

package deployments

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

const (
	// ConfigFileNameMaxLength is the longest ConfigMap data key Kubernetes accepts
	// (DNS-1123 subdomain length).
	ConfigFileNameMaxLength = 253

	// ConfigFilePathMax is Linux PATH_MAX, in bytes. Kubernetes does not bound a
	// mountPath itself; this catches absurd inputs before the kubelet does.
	ConfigFilePathMax = 4096

	// ConfigFileContentMaxBytes bounds a config file's content. The whole ConfigMap
	// object must fit in 1 MiB; this leaves room for its metadata.
	ConfigFileContentMaxBytes = 1<<20 - 4<<10
)

// configFileForbiddenMountRoots are kernel pseudo-filesystems and device nodes;
// shadowing them with a file mount is incoherent.
var configFileForbiddenMountRoots = []string{"/proc", "/sys", "/dev"}

var configFileNamePattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// ConfigFile is one file rendered into a ConfigMap and mounted into the container at
// MountPath/Filename. All three fields are required.
type ConfigFile struct {
	// Filename is the file's name and the ConfigMap key: 1-253 characters of
	// [a-zA-Z0-9._-], not "." or "..", and not starting with "..".
	Filename string `json:"filename"`

	// MountPath is the absolute directory the file is mounted into. It may not be
	// "/", contain ".." segments, or lie under /proc, /sys or /dev.
	MountPath string `json:"mountPath"`

	// Content is the file's content, at most ConfigFileContentMaxBytes of UTF-8.
	Content string `json:"content"`
}

// Validate reports the rules above that are broken. A nil receiver is valid: the file
// is optional.
func (c *ConfigFile) Validate() error {
	if c == nil {
		return nil
	}
	var errs []error
	invalid := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("%w: configFile.%s", ErrInvalidValue,
			fmt.Sprintf(format, args...)))
	}

	switch {
	case c.Filename == "":
		errs = append(errs, MissingError([]string{"configFile.filename"}))
	case len(c.Filename) > ConfigFileNameMaxLength:
		invalid("filename is longer than %d characters", ConfigFileNameMaxLength)
	case !configFileNamePattern.MatchString(c.Filename):
		invalid("filename %q is not [a-zA-Z0-9._-]+", c.Filename)
	case c.Filename == ".", strings.HasPrefix(c.Filename, ".."):
		invalid("filename %q must not be \".\", \"..\" or start with \"..\"", c.Filename)
	}

	cleaned := path.Clean(c.MountPath)
	switch {
	case c.MountPath == "":
		errs = append(errs, MissingError([]string{"configFile.mountPath"}))
	case !strings.HasPrefix(c.MountPath, "/"):
		invalid("mountPath %q is not absolute", c.MountPath)
	case len(c.MountPath) > ConfigFilePathMax:
		invalid("mountPath is longer than %d bytes", ConfigFilePathMax)
	case strings.Contains("/"+c.MountPath+"/", "/../"):
		invalid("mountPath %q contains a \"..\" segment", c.MountPath)
	case cleaned == "/":
		invalid("mountPath must not be the root \"/\"")
	default:
		for _, root := range configFileForbiddenMountRoots {
			if cleaned == root || strings.HasPrefix(cleaned, root+"/") {
				invalid("mountPath %q is under %s", c.MountPath, root)
			}
		}
		if n := len(cleaned) + 1 + len(c.Filename); n > ConfigFilePathMax {
			invalid("mountPath/filename is %d bytes, longer than %d", n, ConfigFilePathMax)
		}
	}

	switch {
	case c.Content == "":
		errs = append(errs, MissingError([]string{"configFile.content"}))
	case len(c.Content) > ConfigFileContentMaxBytes:
		invalid("content is %d bytes, longer than %d", len(c.Content), ConfigFileContentMaxBytes)
	}

	return errors.Join(errs...)
}
