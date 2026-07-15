// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

//go:build !ios && !ts_omit_cachenetmap

package cli

import (
	"context"
	"errors"

	"github.com/peterbourgon/ff/v3/ffcli"
)

func init() {
	debugClearNetmapCacheCmd = func() *ffcli.Command {
		return &ffcli.Command{
			Name:       "clear-netmap-cache",
			ShortUsage: "tailscale debug clear-netmap-cache",
			ShortHelp:  "移除并丢弃已缓存的网络映射（如有）",
			Exec:       runDebugClearNetmapCache,
		}
	}
}

func runDebugClearNetmapCache(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return errors.New("意外的参数")
	}
	return localClient.DebugAction(ctx, "clear-netmap-cache")
}
