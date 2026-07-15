// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"context"
	"flag"
	"fmt"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn"
)

var downCmd = &ffcli.Command{
	Name:       "down",
	ShortUsage: "tailscale down",
	ShortHelp:  "断开与 Tailscale 的连接",

	Exec:    runDown,
	FlagSet: newDownFlagSet(),
}

var downArgs struct {
	acceptedRisks string
	reason        string
}

func newDownFlagSet() *flag.FlagSet {
	downf := newFlagSet("down")
	downf.StringVar(&downArgs.reason, "reason", "", "断开连接的原因，若策略要求则必填")
	registerAcceptRiskFlag(downf, &downArgs.acceptedRisks)
	return downf
}

func runDown(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("过多的非 flag 参数：%q", args)
	}

	if isSSHOverTailscale() {
		if err := presentRiskToUser(riskLoseSSH, `你正通过 Tailscale 连接；此操作将禁用 Tailscale，并导致你的会话断开。`, downArgs.acceptedRisks); err != nil {
			return err
		}
	}

	st, err := localClient.Status(ctx)
	if err != nil {
		return fmt.Errorf("获取当前状态出错：%w", err)
	}
	if st.BackendState == "Stopped" {
		fmt.Fprintf(Stderr, "Tailscale 已经停止。\n")
		return nil
	}
	ctx = apitype.RequestReasonKey.WithValue(ctx, downArgs.reason)
	_, err = localClient.EditPrefs(ctx, &ipn.MaskedPrefs{
		Prefs: ipn.Prefs{
			WantRunning: false,
		},
		WantRunningSet: true,
	})
	return err
}
