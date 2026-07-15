// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package cli

import (
	"context"

	"github.com/peterbourgon/ff/v3/ffcli"
	"tailscale.com/licenses"
)

var licensesCmd = &ffcli.Command{
	Name:       "licenses",
	ShortUsage: "tailscale licenses",
	ShortHelp:  "获取开源许可信息",
	LongHelp:   "获取开源许可信息",
	Exec:       runLicenses,
}

func runLicenses(ctx context.Context, args []string) error {
	url := licenses.LicensesURL()
	outln(`
Tailscale 离不开成千上万开源开发者的贡献。要查看 Tailscale 所包含的开源软件包
及其各自的许可信息，请访问：

    ` + url)
	return nil
}
