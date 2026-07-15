// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/client/tailscale/apitype"
)

var whoisCmd = &ffcli.Command{
	Name:       "whois",
	ShortUsage: "tailscale whois [--json] ip[:port]",
	ShortHelp:  "显示与某个 Tailscale IP（v4 或 v6）关联的主机和用户",
	LongHelp: strings.TrimSpace(`
	'tailscale whois' 显示与某个 Tailscale IP（v4 或 v6）关联的主机和用户。
	`),
	Exec: runWhoIs,
	FlagSet: func() *flag.FlagSet {
		fs := newFlagSet("whois")
		fs.BoolVar(&whoIsArgs.json, "json", false, "以 JSON 格式输出")
		fs.StringVar(&whoIsArgs.proto, "proto", "", `协议；可选 "tcp" 或 "udp"；留空表示两者都包含`)
		return fs
	}(),
}

var whoIsArgs struct {
	json  bool   // output in JSON format
	proto string // "tcp" or "udp"
}

func runWhoIs(ctx context.Context, args []string) error {
	if len(args) > 1 {
		return errors.New("参数过多，最多期望一个对等节点")
	} else if len(args) == 0 {
		return errors.New("缺少参数，期望一个对等节点")
	}
	who, err := localClient.WhoIsProto(ctx, whoIsArgs.proto, args[0])
	if err != nil {
		return err
	}
	return printWhoIs(who, whoIsArgs.json)
}

// printWhoIs prints the WhoIsResponse to Stdout, either as JSON (if asJSON is
// true) or in a human-readable form.
func printWhoIs(who *apitype.WhoIsResponse, asJSON bool) error {
	if asJSON {
		ec := json.NewEncoder(Stdout)
		ec.SetIndent("", "  ")
		ec.Encode(who)
		return nil
	}

	w := tabwriter.NewWriter(Stdout, 10, 5, 5, ' ', 0)
	fmt.Fprintf(w, "主机：\n")
	fmt.Fprintf(w, "  名称：\t%s\n", strings.TrimSuffix(who.Node.Name, "."))
	fmt.Fprintf(w, "  ID：\t%s\n", who.Node.StableID)
	fmt.Fprintf(w, "  地址：\t%s\n", who.Node.Addresses)
	if len(who.Node.AllowedIPs) > 2 {
		fmt.Fprintf(w, "  允许 IP：\t%s\n", who.Node.AllowedIPs[2:])
	}
	if who.Node.IsTagged() {
		fmt.Fprintf(w, "  标签：\t%s\n", strings.Join(who.Node.Tags, ", "))
	} else {
		fmt.Fprintln(w, "用户：")
		fmt.Fprintf(w, "  名称：\t%s\n", who.UserProfile.LoginName)
		fmt.Fprintf(w, "  ID：\t%d\n", who.UserProfile.ID)
	}
	w.Flush()
	w = nil // avoid accidental use

	if cm := who.CapMap; len(cm) > 0 {
		printf("能力：\n")
		for cap, vals := range cm {
			// To make the output more readable, we have to reindent the JSON
			// values so they line up with the cap name.
			if len(vals) > 0 {
				v, _ := json.MarshalIndent(vals, "      ", "  ")

				printf("  - %s：\n", cap)
				printf("      %s\n", v)
			} else {
				printf("  - %s\n", cap)
			}
		}
	}
	return nil
}
