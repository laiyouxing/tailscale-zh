// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/peterbourgon/ff/v3/ffcli"
)

var whoamiCmd = &ffcli.Command{
	Name:       "whoami",
	ShortUsage: "tailscale whoami [--json]",
	ShortHelp:  "显示当前设备的机器与用户身份",
	LongHelp: strings.TrimSpace(`
	'tailscale whoami' 显示当前设备的机器与用户身份。
	它等价于针对当前设备自身的某个 Tailscale IP 地址运行 'tailscale whois'。
	`),
	Exec: runWhoami,
	FlagSet: func() *flag.FlagSet {
		fs := newFlagSet("whoami")
		fs.BoolVar(&whoamiArgs.json, "json", false, "以 JSON 格式输出")
		return fs
	}(),
}

var whoamiArgs struct {
	json bool // output in JSON format
}

func runWhoami(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return errors.New("参数过多，期望无参数")
	}
	st, err := localClient.StatusWithoutPeers(ctx)
	if err != nil {
		return err
	}
	if len(st.TailscaleIPs) == 0 {
		return fmt.Errorf("当前没有 Tailscale IP 地址；状态：%v", st.BackendState)
	}
	who, err := localClient.WhoIsProto(ctx, "", st.TailscaleIPs[0].String())
	if err != nil {
		return err
	}
	return printWhoIs(who, whoamiArgs.json)
}
