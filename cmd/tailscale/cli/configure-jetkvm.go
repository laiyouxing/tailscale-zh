// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build linux && !android && arm

package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"runtime"
	"strings"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/version/distro"
)

func init() {
	maybeJetKVMConfigureCmd = jetKVMConfigureCmd
}

func jetKVMConfigureCmd() *ffcli.Command {
	if runtime.GOOS != "linux" || distro.Get() != distro.JetKVM {
		return nil
	}
	return &ffcli.Command{
		Name:       "jetkvm",
		Exec:       runConfigureJetKVM,
		ShortUsage: "tailscale configure jetkvm",
		ShortHelp:  "配置 JetKVM 在开机时运行 tailscaled",
		LongHelp: strings.TrimSpace(`
此命令配置 JetKVM 主机，使其在开机时运行 tailscaled。
`),
		FlagSet: (func() *flag.FlagSet {
			fs := newFlagSet("jetkvm")
			return fs
		})(),
	}
}

func runConfigureJetKVM(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return errors.New("未知参数")
	}
	if runtime.GOOS != "linux" || distro.Get() != distro.JetKVM {
		return errors.New("仅在 JetKVM 上实现")
	}
	if err := os.MkdirAll("/userdata/init.d", 0755); err != nil {
		return errors.New("无法创建 /userdata/init.d")
	}
	err := os.WriteFile("/userdata/init.d/S22tailscale", bytes.TrimLeft([]byte(`
#!/bin/sh
# /userdata/init.d/S22tailscale
# Start/stop tailscaled

case "$1" in
  start)
    /userdata/tailscale/tailscaled > /dev/null 2>&1 &
    ;;
  stop)
    killall tailscaled
    ;;
  *)
    echo "Usage: $0 {start|stop}"
    exit 1
    ;;
esac
`), "\n"), 0755)
	if err != nil {
		return err
	}

	if err := os.Symlink("/userdata/tailscale/tailscale", "/bin/tailscale"); err != nil {
		if !os.IsExist(err) {
			return err
		}
	}

	printf("完成。现在请重启您的 JetKVM。\n")
	return nil
}
