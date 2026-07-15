// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build !ts_omit_syspolicy

package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"
	"text/tabwriter"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/util/syspolicy/setting"
)

var syspolicyArgs struct {
	json bool // JSON output mode
}

func init() {
	sysPolicyCmd = func() *ffcli.Command {
		return &ffcli.Command{
			Name:       "syspolicy",
			ShortHelp:  "诊断 MDM 与系统策略配置",
			LongHelp:   "'tailscale syspolicy' 命令提供用于诊断 MDM 与系统策略配置的工具。",
			ShortUsage: "tailscale syspolicy <subcommand>",
			UsageFunc:  usageFuncNoDefaultValues,
			Subcommands: []*ffcli.Command{
				{
					Name:       "list",
					ShortUsage: "tailscale syspolicy list",
					Exec:       runSysPolicyList,
					ShortHelp:  "打印生效的策略设置",
					LongHelp:   "'tailscale syspolicy list' 子命令显示生效的策略设置及其来源（例如 MDM 或环境变量）。",
					FlagSet: (func() *flag.FlagSet {
						fs := newFlagSet("syspolicy list")
					fs.BoolVar(&syspolicyArgs.json, "json", false, "以 JSON 格式输出")
					return fs
				})(),
			},
			{
				Name:       "reload",
					ShortUsage: "tailscale syspolicy reload",
					Exec:       runSysPolicyReload,
					ShortHelp:  "强制重新加载策略设置（即使未检测到变更）并打印结果",
					LongHelp:   "'tailscale syspolicy reload' 子命令强制重新加载策略设置，即使未检测到变更，并打印结果。",
					FlagSet: (func() *flag.FlagSet {
						fs := newFlagSet("syspolicy reload")
					fs.BoolVar(&syspolicyArgs.json, "json", false, "以 JSON 格式输出")
					return fs
				})(),
			},
		},
		}
	}
}

func runSysPolicyList(ctx context.Context, args []string) error {
	policy, err := localClient.GetEffectivePolicy(ctx, setting.DefaultScope())
	if err != nil {
		return err
	}
	printPolicySettings(policy)
	return nil
}

func runSysPolicyReload(ctx context.Context, args []string) error {
	policy, err := localClient.ReloadEffectivePolicy(ctx, setting.DefaultScope())
	if err != nil {
		return err
	}
	printPolicySettings(policy)
	return nil
}

func printPolicySettings(policy *setting.Snapshot) {
	if syspolicyArgs.json {
		json, err := json.MarshalIndent(policy, "", "\t")
		if err != nil {
			errf("syspolicy 序列化错误：%v", err)
		} else {
			outln(string(json))
		}
		return
	}
	if policy.Len() == 0 {
		outln("无策略设置")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "名称\t来源\t值\t错误")
	fmt.Fprintln(w, "----\t------\t-----\t-----")
	for _, k := range slices.Sorted(policy.Keys()) {
		setting, _ := policy.GetSetting(k)
		var origin string
		if o := setting.Origin(); o != nil {
			origin = o.String()
		}
		if err := setting.Error(); err != nil {
			fmt.Fprintf(w, "%s\t%s\t\t{%v}\n", k, origin, err)
		} else {
			fmt.Fprintf(w, "%s\t%s\t%v\t\n", k, origin, setting.Value())
		}
	}
	w.Flush()

	fmt.Println()
	return
}
