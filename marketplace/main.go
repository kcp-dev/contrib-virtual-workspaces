/*
Copyright 2026 The kcp Authors.

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

// marketplace-vw is the marketplace component in one binary: `init`
// installs the kcp-side objects, `operator` materializes producer
// workspaces, bind grants and enablement bindings, and `virtualworkspace`
// serves the catalog and the enablements write path. One binary so a
// deployment's init container and both long-running processes share a
// single image.
package main

import (
	"os"

	"github.com/kcp-dev/contrib-virtual-workspaces/marketplace/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
