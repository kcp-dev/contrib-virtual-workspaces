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

// Command scar-to-kubeconfig calls the SCAR endpoint with a bearer token
// and writes a kubeconfig with one context per authorized cluster. The
// same command is mounted in the repo-wide CLI (cmd/cli) as
// `contribctl scar-to-kubeconfig`.
package main

import (
	"os"

	"github.com/kcp-dev/contrib-virtual-workspaces/access/pkg/cli"
)

func main() {
	if err := cli.NewSCARToKubeconfigCommand().Execute(); err != nil {
		os.Exit(1)
	}
}
