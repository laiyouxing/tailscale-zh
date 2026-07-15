// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build !ts_omit_drive && ts_mac_gui

package cli

import (
	"context"
	"errors"

	"github.com/peterbourgon/ff/v3/ffcli"
)

func init() {
	maybeDriveCmd = driveCmdStub
}

func driveCmdStub() *ffcli.Command {
	return &ffcli.Command{
		Name:       "drive",
		ShortHelp:  "与你的 tailnet 上的其他设备共享目录",
		ShortUsage: "tailscale drive [...any]",
		LongHelp:   hidden + "Taildrive 允许你与 tailnet 上的其他设备共享目录。",
		Exec: func(_ context.Context, args []string) error {
			return errors.New(
				"使用 macOS GUI 应用时不支持 Taildrive 命令行命令。" +
					"请使用 Tailscale 菜单栏图标在「设置」中配置 Taildrive。\n\n" +
					"详见 https://tailscale.com/docs/features/taildrive",
			)
		},
	}
}
