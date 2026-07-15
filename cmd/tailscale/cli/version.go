// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/feature"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/version"
)

var versionCmd = &ffcli.Command{
	Name:       "version",
	ShortUsage: "tailscale version [flags]",
	ShortHelp:  "打印 Tailscale 版本",
	FlagSet: (func() *flag.FlagSet {
		fs := newFlagSet("version")
		fs.BoolVar(&versionArgs.daemon, "daemon", false, "同时打印本地节点的守护进程版本")
		fs.BoolVar(&versionArgs.json, "json", false, "以 JSON 格式输出")
		fs.BoolVar(&versionArgs.upstream, "upstream", false, "从 pkgs.tailscale.com 获取并打印最新的上游发布版本")
		fs.StringVar(&versionArgs.track, "track", "", `要检查更新的轨道："stable"、"release-candidate" 或 "unstable"（开发版）；留空表示与当前相同`)
		return fs
	})(),
	Exec: runVersion,
}

var versionArgs struct {
	daemon   bool // also check local node's daemon version
	json     bool
	upstream bool
	track    string
}

var clientupdateLatestTailscaleVersion feature.Hook[func(string) (string, error)]

func runVersion(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("过多的非标志参数：%q", args)
	}
	var err error
	var st *ipnstate.Status

	if versionArgs.daemon {
		st, err = localClient.StatusWithoutPeers(ctx)
		if err != nil {
			return err
		}
	}

	var upstreamVer string
	if versionArgs.upstream {
		f, ok := clientupdateLatestTailscaleVersion.GetOk()
		if !ok {
			return fmt.Errorf("当前构建不支持获取最新版本")
		}
		upstreamVer, err = f(versionArgs.track)
		if err != nil {
			return err
		}
	}

	if versionArgs.json {
		m := version.GetMeta()
		if st != nil {
			m.DaemonLong = st.Version
		}
		out := struct {
			version.Meta
			Upstream string `json:"upstream,omitempty"`
		}{
			Meta:     m,
			Upstream: upstreamVer,
		}
		e := json.NewEncoder(Stdout)
		e.SetIndent("", "\t")
		return e.Encode(out)
	}

	if st == nil {
		outln(version.String())
		if versionArgs.upstream {
			printf("  上游版本：%s\n", upstreamVer)
		}
		return nil
	}
	printf("客户端：%s\n", version.String())
	printf("守护进程：%s\n", st.Version)
	if versionArgs.upstream {
		printf("上游版本：%s\n", upstreamVer)
	}
	return nil
}
