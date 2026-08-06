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

package cmd

import (
	goflag "flag"

	"github.com/spf13/cobra"

	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/kcp-dev/contrib-mcp-virtual-workspace/internal/config"
)

var serverCfg config.ServerConfig

var rootCmd = &cobra.Command{
	Use:   "mcp-virtual-workspace",
	Short: "serve the Model Context Protocol as a kcp virtual workspace",
	// A failing container should print why it failed, not the flag help: cobra
	// prints usage on any RunE error by default, burying the cause.
	SilenceUsage: true,
}

func init() {
	rootCmd.AddCommand(serveCmd)

	klogFlags := goflag.NewFlagSet("klog", goflag.ContinueOnError)
	klog.InitFlags(klogFlags)
	rootCmd.PersistentFlags().AddGoFlagSet(klogFlags)
	serverCfg = config.NewServerConfig()
	serverCfg.AddFlags(rootCmd.PersistentFlags())

	cobra.OnInitialize(initLog)
}

func initLog() { // coverage-ignore
	ctrl.SetLogger(klog.NewKlogr())
}

// Execute runs the root command.
func Execute() { // coverage-ignore
	cobra.CheckErr(rootCmd.Execute())
}
