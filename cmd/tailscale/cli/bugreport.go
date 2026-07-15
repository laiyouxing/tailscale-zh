// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/client/local"
)

var bugReportCmd = &ffcli.Command{
	Name:       "bugreport",
	Exec:       runBugReport,
	ShortHelp:  "打印一个可分享的标识符以帮助诊断问题",
	ShortUsage: "tailscale bugreport [note]",
	FlagSet: (func() *flag.FlagSet {
		fs := newFlagSet("bugreport")
		fs.BoolVar(&bugReportArgs.diagnose, "diagnose", false, "运行额外的深入检查")
		fs.BoolVar(&bugReportArgs.record, "record", false, "若为 true，则暂停后再写入另一条 bug 报告")
		return fs
	})(),
}

var bugReportArgs struct {
	diagnose bool
	record   bool
}

func runBugReport(ctx context.Context, args []string) error {
	var note string
	switch len(args) {
	case 0:
	case 1:
		note = args[0]
	default:
		return errors.New("未知参数")
	}
	opts := local.BugReportOpts{
		Note:     note,
		Diagnose: bugReportArgs.diagnose,
	}
	if !bugReportArgs.record {
		// Simple, non-record case
		logMarker, err := localClient.BugReportWithOpts(ctx, opts)
		if err != nil {
			return err
		}
		outln(logMarker)
		return nil
	}

	// Recording; run the request in the background
	done := make(chan struct{})
	opts.Record = done

	type bugReportResp struct {
		marker string
		err    error
	}
	resCh := make(chan bugReportResp, 1)
	go func() {
		m, err := localClient.BugReportWithOpts(ctx, opts)
		resCh <- bugReportResp{m, err}
	}()

	outln("已开始录制；请复现您的问题，然后按回车键...")
	fmt.Scanln()
	close(done)
	res := <-resCh

	if res.err != nil {
		return res.err
	}

	outln(res.marker)
	outln("请将上面的两条 bug 报告标识符提供给支持团队或在 GitHub issue 中附上。")
	return nil
}
