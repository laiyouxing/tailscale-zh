// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build darwin

package cli

import (
	"context"
	"errors"

	"github.com/peterbourgon/ff/v3/ffcli"
)

func init() {
	maybeSysExtCmd = sysExtCmd
	maybeVPNConfigCmd = vpnConfigCmd
}

// Functions in this file provide a dummy Exec function that only prints an error message for users of the open-source
// tailscaled distribution. On GUI builds, the Swift code in the macOS client handles these commands by not passing the
// flow of execution to the CLI.

// sysExtCmd returns a command for managing the Tailscale system extension on macOS
// (for the Standalone variant of the client only).
func sysExtCmd() *ffcli.Command {
	return &ffcli.Command{
		Name:       "sysext",
		ShortUsage: "tailscale configure sysext [activate|deactivate|status]",
		ShortHelp:  "管理系统扩展（仅限 macOS 的 Standalone 版本）",
		LongHelp: "sysext 这组命令提供了一种方式来激活、停用或管理 macOS 上 Tailscale 系统扩展的状态。" +
			"仅当您运行的是 macOS 版 Tailscale 客户端的 Standalone（独立）变体时才相关。" +
			"若要查看本机上已安装系统扩展的详细信息，请运行 'systemextensionsctl list'。",
		Subcommands: []*ffcli.Command{
			{
				Name:       "activate",
				ShortUsage: "tailscale configure sysext activate",
				ShortHelp:  "向 macOS 注册 Tailscale 系统扩展。",
				LongHelp:   "此命令向 macOS 注册 Tailscale 系统扩展。要运行 Tailscale，您还需单独安装 VPN 配置（运行 `tailscale configure vpn-config install`）。运行此命令后，您需要在“系统设置 > 登录项与扩展 > 网络扩展”中批准该扩展。",
				Exec:       requiresStandalone,
			},
			{
				Name:       "deactivate",
				ShortUsage: "tailscale configure sysext deactivate",
				ShortHelp:  "停用 macOS 上的 Tailscale 系统扩展",
				LongHelp:   "此命令停用 macOS 上的 Tailscale 系统扩展。若要彻底移除 Tailscale，您还需单独删除 VPN 配置（使用 `tailscale configure vpn-config uninstall`）。",
				Exec:       requiresStandalone,
			},
			{
				Name:       "status",
				ShortUsage: "tailscale configure sysext status",
				ShortHelp:  "打印 Tailscale 系统扩展的启用状态",
				LongHelp:   "此命令打印 Tailscale 系统扩展的启用状态。如果扩展未启用，请运行 `tailscale sysext activate` 来启用它。",
				Exec:       requiresStandalone,
			},
		},
		Exec: requiresStandalone,
	}
}

// vpnConfigCmd returns a command for managing the Tailscale VPN configuration on macOS
// (the entry that appears in System Settings > VPN).
func vpnConfigCmd() *ffcli.Command {
	return &ffcli.Command{
		Name:       "mac-vpn",
		ShortUsage: "tailscale configure mac-vpn [install|uninstall]",
		ShortHelp:  "管理 macOS 上的 VPN 配置（App Store 与 Standalone 变体）",
		LongHelp:   "vpn-config 这组命令提供了一种方式，用于从 macOS 设置中添加或移除 Tailscale 的 VPN 配置。这是在“系统设置 > VPN”中出现的条目。",
		Subcommands: []*ffcli.Command{
			{
				Name:       "install",
				ShortUsage: "tailscale configure mac-vpn install",
				ShortHelp:  "将 Tailscale VPN 配置写入 macOS 设置",
				LongHelp:   "此命令将 Tailscale VPN 配置写入 macOS 设置。这是在“系统设置 > VPN”中出现的条目。如果您运行的是客户端的 Standalone（独立）变体，您还需单独安装系统扩展（运行 `tailscale configure sysext activate`）。",
				Exec:       requiresGUI,
			},
			{
				Name:       "uninstall",
				ShortUsage: "tailscale configure mac-vpn uninstall",
				ShortHelp:  "从 macOS 设置中删除 Tailscale VPN 配置",
				LongHelp:   "此命令从 macOS 设置中移除 Tailscale VPN 配置。这是在“系统设置 > VPN”中出现的条目。如果您运行的是客户端的 Standalone（独立）变体，您还需单独停用系统扩展（运行 `tailscale configure sysext deactivate`）。",
				Exec:       requiresGUI,
			},
		},
		Exec: func(ctx context.Context, args []string) error {
			return errors.New("不支持的命令：需要 macOS 客户端的 GUI 版本")
		},
	}
}

func requiresStandalone(ctx context.Context, args []string) error {
	return errors.New("不支持的命令：需要客户端的 Standalone（.pkg 安装包）GUI 版本")
}

func requiresGUI(ctx context.Context, args []string) error {
	return errors.New("不支持的命令：需要 macOS 客户端的 GUI 版本")
}
