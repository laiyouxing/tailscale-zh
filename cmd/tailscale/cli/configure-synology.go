// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/hostinfo"
	"tailscale.com/version/distro"
)

// configureHostCmd is the "tailscale configure-host" command which was once
// used to configure Synology devices, but is now a compatibility alias to
// "tailscale configure synology".
//
// It returns nil if the actual "tailscale configure synology" command is not
// available.
func configureHostCmd() *ffcli.Command {
	synologyConfigureCmd := synologyConfigureCmd()
	if synologyConfigureCmd == nil {
		// No need to offer this compatibility alias if the actual command is not available.
		return nil
	}
	return &ffcli.Command{
		Name:       "configure-host",
		Exec:       runConfigureSynology,
		ShortUsage: "tailscale configure-host\n" + synologyConfigureCmd.ShortUsage,
		ShortHelp:  synologyConfigureCmd.ShortHelp,
		LongHelp:   hidden + synologyConfigureCmd.LongHelp,
		FlagSet: (func() *flag.FlagSet {
			fs := newFlagSet("configure-host")
			return fs
		})(),
	}
}

func synologyConfigureCmd() *ffcli.Command {
	if runtime.GOOS != "linux" || distro.Get() != distro.Synology {
		return nil
	}
	return &ffcli.Command{
		Name:       "synology",
		Exec:       runConfigureSynology,
		ShortUsage: "tailscale configure synology",
		ShortHelp:  "配置 Synology 以启用出站连接",
		LongHelp: strings.TrimSpace(`
此命令用于在 Synology 设备上以 root 身份在启动时运行，以创建设备
/dev/net/tun 并赋予 tailscaled 二进制文件使用它的权限。

参见：https://tailscale.com/s/synology-outbound
`),
		FlagSet: (func() *flag.FlagSet {
			fs := newFlagSet("synology")
			return fs
		})(),
	}
}

func runConfigureSynology(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return errors.New("未知参数")
	}
	if runtime.GOOS != "linux" || distro.Get() != distro.Synology {
		return errors.New("仅在 Synology 上实现")
	}
	if uid := os.Getuid(); uid != 0 {
		return fmt.Errorf("必须以 root 身份运行，而非 %q（%v）", os.Getenv("USER"), uid)
	}
	hi := hostinfo.New()
	isDSM6 := strings.HasPrefix(hi.DistroVersion, "6.")
	isDSM7 := strings.HasPrefix(hi.DistroVersion, "7.")
	if !isDSM6 && !isDSM7 {
		return fmt.Errorf("不支持的 DSM 版本 %q", hi.DistroVersion)
	}
	if _, err := os.Stat("/dev/net/tun"); os.IsNotExist(err) {
		if err := os.MkdirAll("/dev/net", 0755); err != nil {
			return fmt.Errorf("创建 /dev/net：%v", err)
		}
		if out, err := exec.Command("/bin/mknod", "/dev/net/tun", "c", "10", "200").CombinedOutput(); err != nil {
			return fmt.Errorf("创建 /dev/net/tun：%v, %s", err, out)
		}
	}
	if err := os.Chmod("/dev/net", 0755); err != nil {
		return err
	}
	if err := os.Chmod("/dev/net/tun", 0666); err != nil {
		return err
	}
	if isDSM6 {
		printf("/dev/net/tun 已存在且权限为 0666。在 DSM6 上跳过 setcap。\n")
		return nil
	}

	const daemonBin = "/var/packages/Tailscale/target/bin/tailscaled"
	if _, err := os.Stat(daemonBin); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("在 %s 未找到 tailscaled 二进制文件。是否已安装 Tailscale *.spk 软件包？", daemonBin)
		}
		return err
	}
	if out, err := exec.Command("/bin/setcap", "cap_net_admin,cap_net_raw+eip", daemonBin).CombinedOutput(); err != nil {
		return fmt.Errorf("setcap：%v, %s", err, out)
	}
	printf("完成。要重启 Tailscale 以使用新权限，请运行：\n\n  sudo synosystemctl restart pkgctl-Tailscale.service\n\n")
	return nil
}
