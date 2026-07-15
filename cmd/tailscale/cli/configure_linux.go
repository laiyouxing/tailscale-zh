// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build linux && !ts_omit_systray

package cli

import (
	"context"
	"flag"
	"fmt"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/client/systray"
)

func init() {
	maybeSystrayCmd = systrayConfigCmd
}

var configSystrayArgs struct {
	initSystem     string
	installStartup bool
}

func systrayConfigCmd() *ffcli.Command {
	return &ffcli.Command{
		Name:       "systray",
		ShortUsage: "tailscale configure systray [options]",
		ShortHelp:  "[ALPHA] 管理 Linux 的系统托盘客户端",
		LongHelp:   "[ALPHA] systray 这组命令提供了一种方式来配置 Linux 上的系统托盘应用程序。",
		Exec:       configureSystray,
		FlagSet: (func() *flag.FlagSet {
			fs := newFlagSet("systray")
			fs.StringVar(&configSystrayArgs.initSystem, "enable-startup", "",
				"为初始化系统安装开机启动脚本。当前支持的系统为 [systemd, freedesktop]。")
			return fs
		})(),
	}
}

func configureSystray(_ context.Context, _ []string) error {
	if configSystrayArgs.initSystem != "" {
		if err := systray.InstallStartupScript(configSystrayArgs.initSystem); err != nil {
			fmt.Printf("%s\n\n", err.Error())
			return flag.ErrHelp
		}
		return nil
	}
	return flag.ErrHelp
}
