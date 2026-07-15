// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"context"
	"errors"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/envknob"
)

var idTokenCmd = &ffcli.Command{
	Name:       "id-token",
	ShortUsage: "tailscale id-token <aud>",
	ShortHelp:  "为 Tailscale 设备获取一个 OIDC id-token",
	LongHelp:   hidden,
	Exec:       runIDToken,
}

func runIDToken(ctx context.Context, args []string) error {
	if !envknob.UseWIPCode() {
		return errors.New("tailscale id-token: 进行中的功能需要设置环境变量 TAILSCALE_USE_WIP_CODE=1")
	}
	if len(args) != 1 {
		return errors.New("用法: tailscale id-token <aud>")
	}

	tr, err := localClient.IDToken(ctx, args[0])
	if err != nil {
		return err
	}

	outln(tr.IDToken)
	return nil
}
