// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/cmd/tailscale/cli/ffcomplete"
	"tailscale.com/ipn"
)

var switchCmd = &ffcli.Command{
	Name: "switch",
	ShortUsage: strings.Join([]string{
		"tailscale switch <id>",
		"tailscale switch --list [--json]",
	}, "\n"),
	ShortHelp: "切换到不同的 Tailscale 账户",
	LongHelp: `"tailscale switch" 在已登录的账户之间切换。你可以
使用从 'tailnet switch -list' 返回的 ID
来选择要切换到的配置。此外，你也可以
使用 Tailnet、账户名或显示名来切换。

此命令目前处于 alpha 阶段，未来可能发生变化。`,

	FlagSet: func() *flag.FlagSet {
		fs := flag.NewFlagSet("switch", flag.ExitOnError)
		fs.BoolVar(&switchArgs.list, "list", false, "列出可用的账户")
		fs.BoolVar(&switchArgs.json, "json", false, "以 JSON 格式列出可用账户")
		return fs
	}(),
	Exec: switchProfile,

	// Add remove subcommand
	Subcommands: []*ffcli.Command{
		{
			Name:       "remove",
			ShortUsage: "tailscale switch remove <id>",
			ShortHelp:  "移除一个 Tailscale 账户",
			LongHelp: `"tailscale switch remove" 从本机移除一个 Tailscale 账户。
这不会删除账户本身，但它将不再可用于切换。
你可以重新登录来将其加回。

此命令目前处于 alpha 阶段，未来可能发生变化。`,
			Exec: removeProfile,
		},
	},
}

func init() {
	ffcomplete.Args(switchCmd, func(s []string) (words []string, dir ffcomplete.ShellCompDirective, err error) {
		_, all, err := localClient.ProfileStatus(context.Background())
		if err != nil {
			return nil, 0, err
		}

		seen := make(map[string]bool, 3*len(all))
		wordfns := []func(prof ipn.LoginProfile) string{
			func(prof ipn.LoginProfile) string { return string(prof.ID) },
			func(prof ipn.LoginProfile) string { return prof.NetworkProfile.DisplayNameOrDefault() },
			func(prof ipn.LoginProfile) string { return prof.Name },
		}

		for _, wordfn := range wordfns {
			for _, prof := range all {
				word := wordfn(prof)
				if seen[word] {
					continue
				}
				seen[word] = true
				words = append(words, fmt.Sprintf("%s\tid：%s，tailnet：%s，account：%s", word, prof.ID, prof.NetworkProfile.DisplayNameOrDefault(), prof.Name))
			}
		}
		return words, ffcomplete.ShellCompDirectiveNoFileComp, nil
	})
}

var switchArgs struct {
	list bool
	json bool
}

func listProfiles(ctx context.Context) error {
	curP, all, err := localClient.ProfileStatus(ctx)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(Stdout, 2, 2, 2, ' ', 0)
	defer tw.Flush()
	printRow := func(vals ...string) {
		fmt.Fprintln(tw, strings.Join(vals, "\t"))
	}
	printRow("ID", "Tailnet", "账户")
	for _, prof := range all {
		name := prof.Name
		if prof.ID == curP.ID {
			name += "*"
		}
		printRow(
			string(prof.ID),
			prof.NetworkProfile.DisplayNameOrDefault(),
			name,
		)
	}
	return nil
}

type switchProfileJSON struct {
	ID       string `json:"id"`
	Nickname string `json:"nickname"`
	Tailnet  string `json:"tailnet"`
	Account  string `json:"account"`
	Selected bool   `json:"selected"`
}

func listProfilesJSON(ctx context.Context) error {
	curP, all, err := localClient.ProfileStatus(ctx)
	if err != nil {
		return err
	}
	profiles := make([]switchProfileJSON, 0, len(all))
	for _, prof := range all {
		profiles = append(profiles, switchProfileJSON{
			ID:       string(prof.ID),
			Tailnet:  prof.NetworkProfile.DisplayNameOrDefault(),
			Account:  prof.UserProfile.LoginName,
			Nickname: prof.Name,
			Selected: prof.ID == curP.ID,
		})
	}
	profilesJSON, err := json.MarshalIndent(profiles, "", "  ")
	if err != nil {
		return err
	}
	printf("%s\n", profilesJSON)
	return nil
}

func switchProfile(ctx context.Context, args []string) error {
	if switchArgs.list {
		if switchArgs.json {
			return listProfilesJSON(ctx)
		}
		return listProfiles(ctx)
	}
	if switchArgs.json {
		outln("--json 参数不能与 tailscale switch NAME 一起使用")
		os.Exit(1)
	}
	if len(args) != 1 {
		outln("用法：tailscale switch NAME")
		os.Exit(1)
	}
	cp, all, err := localClient.ProfileStatus(ctx)
	if err != nil {
		errf("切换到账户失败：%v\n", err)
		os.Exit(1)
	}
	profID, ok := matchProfile(args[0], all)
	if !ok {
		errf("没有名为 %q 的配置\n", args[0])
		os.Exit(1)
	}
	if profID == cp.ID {
		printf("已在账户 %q\n", args[0])
		os.Exit(0)
	}
	if err := localClient.SwitchProfile(ctx, profID); err != nil {
		errf("切换到账户失败：%v\n", err)
		os.Exit(1)
	}
	printf("正在切换到账户 %q\n", args[0])
	for {
		select {
		case <-ctx.Done():
			errf("等待切换完成超时。")
			os.Exit(1)
		default:
		}
		st, err := localClient.StatusWithoutPeers(ctx)
		if err != nil {
			errf("获取状态出错：%v", err)
			os.Exit(1)
		}
		switch st.BackendState {
		case "NoState", "Starting":
			// TODO(maisem): maybe add a way to subscribe to state changes to
			// LocalClient.
			time.Sleep(100 * time.Millisecond)
			continue
		case "NeedsLogin":
			outln("已登出。")
			outln("要登录，请运行：")
			outln("  tailscale up")
			return nil
		case "Running":
			outln("成功。")
			return nil
		}
		// For all other states, use the default error message.
		if msg, ok := isRunningOrStarting(st); !ok {
			outln(msg)
			os.Exit(1)
		}
	}
}

func removeProfile(ctx context.Context, args []string) error {
	if len(args) != 1 {
		outln("用法：tailscale switch remove NAME")
		os.Exit(1)
	}
	cp, all, err := localClient.ProfileStatus(ctx)
	if err != nil {
		errf("移除账户失败：%v\n", err)
		os.Exit(1)
	}

	profID, ok := matchProfile(args[0], all)
	if !ok {
		errf("没有名为 %q 的配置\n", args[0])
		os.Exit(1)
	}

	if profID == cp.ID {
		printf("已在账户 %q\n", args[0])
		os.Exit(0)
	}

	return localClient.DeleteProfile(ctx, profID)
}

func matchProfile(arg string, all []ipn.LoginProfile) (ipn.ProfileID, bool) {
	// Allow matching by ID, Tailnet, Account, or Display Name
	// in that order.
	for _, p := range all {
		if p.ID == ipn.ProfileID(arg) {
			return p.ID, true
		}
	}
	for _, p := range all {
		if p.NetworkProfile.DomainName == arg {
			return p.ID, true
		}
	}
	for _, p := range all {
		if p.Name == arg {
			return p.ID, true
		}
	}
	for _, p := range all {
		if p.NetworkProfile.DisplayName == arg {
			return p.ID, true
		}
	}
	return "", false
}
