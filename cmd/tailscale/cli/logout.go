// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/client/tailscale/apitype"
)

var logoutArgs struct {
	reason string
}

var logoutCmd = &ffcli.Command{
	Name:       "logout",
	ShortUsage: "tailscale logout",
	ShortHelp:  "断开与 Tailscale 的连接并吊销当前节点密钥",

	LongHelp: strings.TrimSpace(`
"tailscale logout" 会断开网络并使当前节点密钥失效，
日后再次使用时会强制重新验证身份。
`),
	Exec: runLogout,
	FlagSet: (func() *flag.FlagSet {
		fs := newFlagSet("logout")
		fs.StringVar(&logoutArgs.reason, "reason", "", "登出的原因，若策略要求则必填")
		return fs
	})(),
}

func runLogout(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("过多的非 flag 参数：%q", args)
	}
	ctx = apitype.RequestReasonKey.WithValue(ctx, logoutArgs.reason)
	return localClient.Logout(ctx)
}
