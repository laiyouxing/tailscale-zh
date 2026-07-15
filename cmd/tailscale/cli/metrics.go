// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/atomicfile"
)

var metricsCmd = &ffcli.Command{
	Name:      "metrics",
		ShortHelp: "显示 Tailscale 指标",
	LongHelp: strings.TrimSpace(`

'tailscale metrics' 命令显示 Tailscale 面向用户的指标（与
'tailscale debug metrics' 打印的内部指标不同）。

关于 Tailscale 指标的更多信息，请参阅
https://tailscale.com/s/client-metrics

`),
	ShortUsage: "tailscale metrics <subcommand> [flags]",
	UsageFunc:  usageFuncNoDefaultValues,
	Exec:       runMetricsNoSubcommand,
	Subcommands: []*ffcli.Command{
		{
			Name:       "print",
			ShortUsage: "tailscale metrics print",
			Exec:       runMetricsPrint,
			ShortHelp:  "以 Prometheus 文本格式打印当前指标值",
		},
		{
			Name:       "write",
			ShortUsage: "tailscale metrics write <path>",
			Exec:       runMetricsWrite,
			ShortHelp:  "将指标值写入文件",
			LongHelp: strings.TrimSpace(`

'tailscale metrics write' 命令将指标值写入由其唯一参数指定的文本文件。它旨在与
Prometheus node exporter 配合使用，使 Tailscale 指标能够被 textfile collector 采集并导出。

举例来说，要在运行 node exporter 的 Ubuntu 系统上导出 Tailscale 指标，你可以
通过 cron 或 systemd 定时器定期运行
'tailscale metrics write /var/lib/prometheus/node-exporter/tailscaled.prom'。

	`),
		},
	},
}

// runMetricsNoSubcommand prints metric values if no subcommand is specified.
func runMetricsNoSubcommand(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("tailscale metrics: 未知子命令: %s", args[0])
	}

	return runMetricsPrint(ctx, args)
}

// runMetricsPrint prints metric values to stdout.
func runMetricsPrint(ctx context.Context, args []string) error {
	out, err := localClient.UserMetrics(ctx)
	if err != nil {
		return err
	}
	Stdout.Write(out)
	return nil
}

// runMetricsWrite writes metric values to a file.
func runMetricsWrite(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("用法: tailscale metrics write <path>")
	}
	path := args[0]
	out, err := localClient.UserMetrics(ctx)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, out, 0644)
}
