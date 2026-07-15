// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"context"
	"flag"

	"github.com/peterbourgon/ff/v3/ffcli"
)

var loginArgs upArgsT

var loginCmd = &ffcli.Command{
	Name:       "login",
	ShortUsage: "tailscale login [flags]",
	ShortHelp:  "登录到 Tailscale 账户",
	LongHelp: `"tailscale login" 将此主机登录到你的 Tailscale 网络。
此命令目前处于 alpha 阶段，未来可能发生变化。`,
	FlagSet: func() *flag.FlagSet {
		return newUpFlagSet(effectiveGOOS(), &loginArgs, "login")
	}(),
	Exec: func(ctx context.Context, args []string) error {
		if err := localClient.SwitchToEmptyProfile(ctx); err != nil {
			return err
		}
		return runUp(ctx, "login", args, loginArgs)
	},
}
