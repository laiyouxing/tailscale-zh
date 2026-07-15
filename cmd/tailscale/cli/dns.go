// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"strings"

	"github.com/peterbourgon/ff/v3/ffcli"
)

var dnsCmd = &ffcli.Command{
	Name:      "dns",
	ShortHelp: "诊断内部 DNS 转发器",
	LongHelp: strings.TrimSpace(`
'tailscale dns' 子命令提供用于诊断内部 DNS 转发器
（100.100.100.100）的工具。

有关 Tailscale 内置 DNS 功能的更多信息，请参阅
https://tailscale.com/kb/1054/dns。
`),
	ShortUsage: strings.Join([]string{
		dnsStatusCmd.ShortUsage,
		dnsQueryCmd.ShortUsage,
	}, "\n"),
	UsageFunc: usageFuncNoDefaultValues,
	Subcommands: []*ffcli.Command{
		dnsStatusCmd,
		dnsQueryCmd,

		// TODO: implement `tailscale log` here

		// The above work is tracked in https://github.com/tailscale/tailscale/issues/13326
	},
}
