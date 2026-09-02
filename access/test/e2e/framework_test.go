//go:build e2e

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

package e2e

// The e2e framework is shared between the virtual workspace suites and lives
// in test/e2e/framework. This file binds it to this suite: it embeds the
// suite's own manifest templates and aliases the helpers so the tests read
// the same as they did when the framework was package-local.

import (
	"embed"

	"github.com/kcp-dev/contrib-virtual-workspaces/test/e2e/framework"
)

//go:embed testdata/*.yaml.tmpl
var testdata embed.FS

func init() { framework.Testdata = testdata }

type cluster = framework.Cluster

var (
	newCluster         = framework.NewCluster
	workloadCluster    = framework.WorkloadCluster
	render             = framework.Render
	splitYAML          = framework.SplitYAML
	createNamespace    = framework.CreateNamespace
	waitForPods        = framework.WaitForPods
	waitForSecret      = framework.WaitForSecret
	portForward        = framework.PortForward
	kcpConfig          = framework.KCPConfig
	inWorkspace        = framework.InWorkspace
	createWorkspace    = framework.CreateWorkspace
	schemaGVR          = framework.SchemaGVR
	logicalClusterName = framework.LogicalClusterName
	splitImage         = framework.SplitImage
)
