// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"flag"
	"strings"

	"github.com/peterbourgon/ff/v3/ffcli"
)

var (
	maybeJetKVMConfigureCmd,
	maybeConfigSynologyCertCmd,
	_ func() *ffcli.Command // non-nil only on Linux/arm for JetKVM
)

func configureCmd() *ffcli.Command {
	return &ffcli.Command{
		Name:       "configure",
		ShortUsage: "tailscale configure <subcommand>",
		ShortHelp:  "配置主机以启用更多 Tailscale 功能",
		LongHelp: strings.TrimSpace(`
'configure' 这组命令旨在提供一种方式，用于在主机上启用不同的服务，
从而以更多方式使用 Tailscale。
`),
		FlagSet: (func() *flag.FlagSet {
			fs := newFlagSet("configure")
			return fs
		})(),
		Subcommands: nonNilCmds(
			configureKubeconfigCmd(),
			synologyConfigureCmd(),
			flashApplianceCmd(),
			pveApplianceCmd(),
			ccall(maybeConfigSynologyCertCmd),
			ccall(maybeSysExtCmd),
			ccall(maybeVPNConfigCmd),
			ccall(maybeJetKVMConfigureCmd),
			ccall(maybeSystrayCmd),
		),
	}
}

// ccall calls the function f if it is non-nil, and returns its result.
//
// It returns the zero value of the type T if f is nil.
func ccall[T any](f func() T) T {
	var zero T
	if f == nil {
		return zero
	}
	return f()
}
