// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build !ts_omit_clientupdate

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"runtime"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/clientupdate"
	"tailscale.com/util/prompt"
	"tailscale.com/version"
	"tailscale.com/version/distro"
)

func init() {
	maybeUpdateCmd = func() *ffcli.Command { return updateCmd }

	clientupdateLatestTailscaleVersion.Set(func(track string) (string, error) {
		if track == "" {
			return clientupdate.LatestTailscaleVersion(clientupdate.CurrentTrack)
		}
		return clientupdate.LatestTailscaleVersion(track)
	})
}

var updateCmd = &ffcli.Command{
	Name:       "update",
	ShortUsage: "tailscale update",
	ShortHelp:  "将 Tailscale 更新到最新/其他版本",
	Exec:       runUpdate,
	FlagSet: (func() *flag.FlagSet {
		fs := newFlagSet("update")
		fs.BoolVar(&updateArgs.yes, "yes", false, "不进行交互式提示直接更新")
		fs.BoolVar(&updateArgs.dryRun, "dry-run", false, "仅打印更新将执行的操作而不实际执行，也不提示")
		// These flags are not supported on several systems that only provide
		// the latest version of Tailscale:
		//
		//  - Arch (and other pacman-based distros)
		//  - Alpine (and other apk-based distros)
		//  - FreeBSD (and other pkg-based distros)
		//  - Unraid/QNAP/Synology
		//  - macOS
		if distro.Get() != distro.Arch &&
			distro.Get() != distro.Alpine &&
			distro.Get() != distro.QNAP &&
			distro.Get() != distro.Synology &&
			runtime.GOOS != "freebsd" &&
			runtime.GOOS != "darwin" {
			fs.StringVar(&updateArgs.track, "track", "", `要检查更新的轨道："stable"、"release-candidate" 或 "unstable"（开发版）；留空表示与当前相同`)
			fs.StringVar(&updateArgs.version, "version", "", `要更新/降级到的明确版本`)
		}
		return fs
	})(),
}

var updateArgs struct {
	yes     bool
	dryRun  bool
	track   string // explicit track; empty means same as current
	version string // explicit version; empty means auto
}

const gokrazyUpdateFromURLMagicArg = "--gokrazy-update-from-url"

func runUpdate(ctx context.Context, args []string) error {
	if len(args) > 0 {
		if runtime.GOOS == "linux" && distro.Get() == distro.Gokrazy {
			gokArgs, err := gokrazyUpdateArgsFromMagicArg(args)
			if err != nil {
				return err
			}
			if gokArgs != nil {
				return clientupdate.GokrazyUpdateFromURL.Get()(ctx, *gokArgs)
			}
		}
		return flag.ErrHelp
	}
	if updateArgs.version != "" && updateArgs.track != "" {
		return errors.New("不能同时指定 --version 和 --track")
	}
	err := clientupdate.Update(clientupdate.Arguments{
		Version: updateArgs.version,
		Track:   updateArgs.track,
		Logf:    func(f string, a ...any) { printf(f+"\n", a...) },
		Stdout:  Stdout,
		Stderr:  Stderr,
		Confirm: confirmUpdate,
	})
	if errors.Is(err, errors.ErrUnsupported) {
		return errors.New("此平台不支持 'update' 命令；请参阅 https://tailscale.com/s/client-updates")
	}
	return err
}

func confirmUpdate(ver string) bool {
	if updateArgs.yes {
		fmt.Printf("正在将 Tailscale 从 %v 更新到 %v；已给定 --yes，不进行提示直接继续。\n", version.Short(), ver)
		return true
	}

	if updateArgs.dryRun {
		fmt.Printf("当前：%v，最新：%v\n", version.Short(), ver)
		return false
	}

	msg := fmt.Sprintf("这将把 Tailscale 从 %v 更新到 %v。是否继续？", version.Short(), ver)
	return prompt.YesNo(msg, true)
}

// gokrazyUpdateArgsFromMagicArg parses the Gokrazy update-from-URL command-line
// flow. It returns nil if args do not select that flow. A non-nil result means
// the caller may safely invoke clientupdate.GokrazyUpdateFromURL.
func gokrazyUpdateArgsFromMagicArg(args []string) (*clientupdate.GokrazyUpdateArgs, error) {
	var updateURL string
	var unsigned bool

	fs := flag.NewFlagSet("gokrazy-update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	// This flag path is exercised end-to-end by TestGokrazyUpdatesItselfToSameImage.
	fs.StringVar(&updateURL, gokrazyUpdateFromURLMagicArg[2:], "", "要安装的 Gokrazy 归档格式文件的 URL")
	fs.BoolVar(&unsigned, "unsigned", false, "跳过 GAF 签名校验；仅用于测试")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() != 0 {
		return nil, nil
	}
	if updateURL == "" {
		return nil, nil
	}
	if !clientupdate.GokrazyUpdateFromURL.IsSet() {
		return nil, errors.New("gokrazy update support is not linked into this binary")
	}
	return &clientupdate.GokrazyUpdateArgs{
		URL:           updateURL,
		AllowUnsigned: unsigned,
		Logf: func(format string, args ...any) {
			printf(format+"\n", args...)
		},
	}, nil
}
