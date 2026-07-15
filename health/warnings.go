// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package health

import (
	"fmt"
	"runtime"
	"time"

	"tailscale.com/feature/buildfeatures"
	"tailscale.com/tsconst"
	"tailscale.com/version"
)

func condRegister(f func() *Warnable) *Warnable {
	if !buildfeatures.HasHealth {
		return nil
	}
	return f()
}

/**
This file contains definitions for the Warnables maintained within this `health` package.
*/

// updateAvailableWarnable is a Warnable that warns the user that an update is available.
var updateAvailableWarnable = condRegister(func() *Warnable {
	return &Warnable{
		Code:     tsconst.HealthWarnableUpdateAvailable,
		Title:    "有可用更新",
		Severity: SeverityLow,
		Text: func(args Args) string {
			if version.IsMacAppStore() || version.IsAppleTV() || version.IsMacSys() || version.IsWindowsGUI() || runtime.GOOS == "android" {
				return fmt.Sprintf("有可用更新：从版本 %s 到 %s。", args[ArgCurrentVersion], args[ArgAvailableVersion])
			} else {
				return fmt.Sprintf("有可用更新：从版本 %s 到 %s。运行 `tailscale update` 或 `tailscale set --auto-update` 立即更新。", args[ArgCurrentVersion], args[ArgAvailableVersion])
			}
		},
	}
})

// securityUpdateAvailableWarnable is a Warnable that warns the user that an important security update is available.
var securityUpdateAvailableWarnable = condRegister(func() *Warnable {
	return &Warnable{
		Code:     tsconst.HealthWarnableSecurityUpdateAvailable,
		Title:    "有可用安全更新",
		Severity: SeverityMedium,
		Text: func(args Args) string {
			if version.IsMacAppStore() || version.IsAppleTV() || version.IsMacSys() || version.IsWindowsGUI() || runtime.GOOS == "android" {
				return fmt.Sprintf("有可用安全更新：从版本 %s 到 %s。", args[ArgCurrentVersion], args[ArgAvailableVersion])
			} else {
				return fmt.Sprintf("有可用安全更新：从版本 %s 到 %s。运行 `tailscale update` 或 `tailscale set --auto-update` 立即更新。", args[ArgCurrentVersion], args[ArgAvailableVersion])
			}
		},
	}
})

// unstableWarnable is a Warnable that warns the user that they are using an unstable version of Tailscale
// so they won't be surprised by all the issues that may arise.
var unstableWarnable = condRegister(func() *Warnable {
	return &Warnable{
		Code:     tsconst.HealthWarnableIsUsingUnstableVersion,
		Title:    "使用的是不稳定版本",
		Severity: SeverityLow,
		Text:     StaticMessage("这是用于测试与开发的 Tailscale 不稳定版本，仅供测试与开发用途。如有问题请向 Tailscale 反馈。"),
	}
})

// NetworkStatusWarnable is a Warnable that warns the user that the network is down.
var NetworkStatusWarnable = condRegister(func() *Warnable {
	return &Warnable{
		Code:                tsconst.HealthWarnableNetworkStatus,
		Title:               "网络已断开",
		Severity:            SeverityMedium,
		Text:                StaticMessage("Tailscale 无法连接，因为网络已断开。请检查你的 Internet 网络连接。"),
		ImpactsConnectivity: true,
		TimeToVisible:       5 * time.Second,
	}
})

// IPNStateWarnable is a Warnable that warns the user that Tailscale is stopped.
var IPNStateWarnable = condRegister(func() *Warnable {
	return &Warnable{
		Code:     tsconst.HealthWarnableWantRunningFalse,
		Title:    "Tailscale 已关闭",
		Severity: SeverityLow,
		Text:     StaticMessage("Tailscale 已停止运行。"),
	}
})

// localLogWarnable is a Warnable that warns the user that the local log is misconfigured.
var localLogWarnable = condRegister(func() *Warnable {
	return &Warnable{
		Code:     tsconst.HealthWarnableLocalLogConfigError,
		Title:    "本地日志配置错误",
		Severity: SeverityLow,
		Text: func(args Args) string {
			return fmt.Sprintf("本地日志配置不正确：%v", args[ArgError])
		},
	}
})

// LoginStateWarnable is a Warnable that warns the user that they are logged out,
// and provides the last login error if available.
var LoginStateWarnable = condRegister(func() *Warnable {
	return &Warnable{
		Code:     tsconst.HealthWarnableLoginState,
		Title:    "已退出登录",
		Severity: SeverityMedium,
		Text: func(args Args) string {
			if args[ArgError] != "" {
				return fmt.Sprintf("你已退出登录。上次登录错误为：%v", args[ArgError])
			} else {
				return "你已退出登录。"
			}
		},
		DependsOn: []*Warnable{IPNStateWarnable},
	}
})

// notInMapPollWarnable is a Warnable that warns the user that we are using a stale network map.
var notInMapPollWarnable = condRegister(func() *Warnable {
	return &Warnable{
		Code:      tsconst.HealthWarnableNotInMapPoll,
		Title:     "状态不同步",
		Severity:  SeverityMedium,
		DependsOn: []*Warnable{NetworkStatusWarnable, IPNStateWarnable},
		Text:      StaticMessage("无法连接到 Tailscale 协调服务器以同步你的 tailnet 状态。节点间的可达性可能会随时间下降。"),
		// 8 minutes reflects a maximum maintenance window for the coordination server.
		TimeToVisible: 8 * time.Minute,
	}
})

// noDERPHomeWarnable is a Warnable that warns the user that Tailscale doesn't have a home DERP.
var noDERPHomeWarnable = condRegister(func() *Warnable {
	return &Warnable{
		Code:                tsconst.HealthWarnableNoDERPHome,
		Title:               "无主中继服务器",
		Severity:            SeverityMedium,
		DependsOn:           []*Warnable{NetworkStatusWarnable},
		Text:                StaticMessage("Tailscale 无法连接到任何中继服务器。请检查你的 Internet 网络连接。"),
		ImpactsConnectivity: true,
		TimeToVisible:       10 * time.Second,
	}
})

// noDERPConnectionWarnable is a Warnable that warns the user that Tailscale couldn't connect to a specific DERP server.
var noDERPConnectionWarnable = condRegister(func() *Warnable {
	return &Warnable{
		Code:     tsconst.HealthWarnableNoDERPConnection,
		Title:    "中继服务器不可用",
		Severity: SeverityMedium,
		DependsOn: []*Warnable{
			NetworkStatusWarnable,

			// Technically noDERPConnectionWarnable could be used to warn about
			// failure to connect to a specific DERP server (e.g. your home is derp1
			// but you're trying to connect to a peer's derp4 and are unable) but as
			// of 2024-09-25 we only use this for connecting to your home DERP, so
			// we depend on noDERPHomeWarnable which is the ability to figure out
			// what your DERP home even is.
			noDERPHomeWarnable,
		},
		Text: func(args Args) string {
			if n := args[ArgDERPRegionName]; n != "" {
				return fmt.Sprintf("Tailscale 无法连接到 '%s' 中继服务器。你的 Internet 连接可能已断开，或该服务器可能暂时不可用。", n)
			} else {
				return fmt.Sprintf("Tailscale 无法连接到 ID 为 '%s' 的中继服务器。你的 Internet 连接可能已断开，或该服务器可能暂时不可用。", args[ArgDERPRegionID])
			}
		},
		ImpactsConnectivity: true,
		TimeToVisible:       10 * time.Second,
	}
})

// derpTimeoutWarnable is a Warnable that warns the user that Tailscale hasn't
// heard from the home DERP region for a while.
var derpTimeoutWarnable = condRegister(func() *Warnable {
	return &Warnable{
		Code:     tsconst.HealthWarnableDERPTimedOut,
		Title:    "中继服务器超时",
		Severity: SeverityMedium,
		DependsOn: []*Warnable{
			NetworkStatusWarnable,
			noDERPConnectionWarnable, // don't warn about it being stalled if we're not connected
			noDERPHomeWarnable,       // same reason as noDERPConnectionWarnable's dependency
		},
		Text: func(args Args) string {
			if n := args[ArgDERPRegionName]; n != "" {
				return fmt.Sprintf("Tailscale 已有 %v 没有收到 '%s' 中继服务器的消息。该服务器可能暂时不可用，或你的 Internet 连接可能已断开。", args[ArgDuration], n)
			} else {
				return fmt.Sprintf("Tailscale 已有 %v 没有收到主中继服务器（区域 ID '%v'）的消息。该服务器可能暂时不可用，或你的 Internet 连接可能已断开。", args[ArgDuration], args[ArgDERPRegionID])
			}
		},
	}
})

// derpRegionErrorWarnable is a Warnable that warns the user that a DERP region is reporting an issue.
var derpRegionErrorWarnable = condRegister(func() *Warnable {
	return &Warnable{
		Code:      tsconst.HealthWarnableDERPRegionError,
		Title:     "中继服务器错误",
		Severity:  SeverityLow,
		DependsOn: []*Warnable{NetworkStatusWarnable},
		Text: func(args Args) string {
			return fmt.Sprintf("中继服务器 #%v 报告了一个问题：%v", args[ArgDERPRegionID], args[ArgError])
		},
	}
})

// noUDP4BindWarnable is a Warnable that warns the user that Tailscale couldn't listen for incoming UDP connections.
var noUDP4BindWarnable = condRegister(func() *Warnable {
	return &Warnable{
		Code:                tsconst.HealthWarnableNoUDP4Bind,
		Title:               "NAT 穿透设置失败",
		Severity:            SeverityMedium,
		DependsOn:           []*Warnable{NetworkStatusWarnable, IPNStateWarnable},
		Text:                StaticMessage("Tailscale 无法监听传入的 UDP 连接。"),
		ImpactsConnectivity: true,
	}
})

// mapResponseTimeoutWarnable is a Warnable that warns the user that Tailscale hasn't received a network map from the coordination server in a while.
var mapResponseTimeoutWarnable = condRegister(func() *Warnable {
	return &Warnable{
		Code:      tsconst.HealthWarnableMapResponseTimeout,
		Title:     "网络映射响应超时",
		Severity:  SeverityMedium,
		DependsOn: []*Warnable{NetworkStatusWarnable, IPNStateWarnable},
		Text: func(args Args) string {
			return fmt.Sprintf("Tailscale 已有 %s 没有收到来自协调服务器的网络映射。", args[ArgDuration])
		},
	}
})

// tlsConnectionFailedWarnable is a Warnable that warns the user that Tailscale could not establish an encrypted connection with a server.
var tlsConnectionFailedWarnable = condRegister(func() *Warnable {
	return &Warnable{
		Code:      tsconst.HealthWarnableTLSConnectionFailed,
		Title:     "加密连接失败",
		Severity:  SeverityMedium,
		DependsOn: []*Warnable{NetworkStatusWarnable},
		Text: func(args Args) string {
			return fmt.Sprintf("Tailscale 无法与 '%q' 建立加密连接：%v", args[ArgServerName], args[ArgError])
		},
	}
})

// magicsockReceiveFuncWarnable is a Warnable that warns the user that one of the Magicsock functions is not running.
var magicsockReceiveFuncWarnable = condRegister(func() *Warnable {
	return &Warnable{
		Code:     tsconst.HealthWarnableMagicsockReceiveFuncError,
		Title:    "MagicSock 函数未运行",
		Severity: SeverityMedium,
		Text: func(args Args) string {
			return fmt.Sprintf("MagicSock 函数 %s 未运行。你可能会遇到连接问题。", args[ArgMagicsockFunctionName])
		},
	}
})

// testWarnable is a Warnable that is used within this package for testing purposes only.
var testWarnable = condRegister(func() *Warnable {
	return &Warnable{
		Code:     tsconst.HealthWarnableTestWarnable,
		Title:    "测试告警项",
		Severity: SeverityLow,
		Text: func(args Args) string {
			return args[ArgError]
		},
	}
})

// applyDiskConfigWarnable is a Warnable that warns the user that there was an error applying the envknob config stored on disk.
var applyDiskConfigWarnable = condRegister(func() *Warnable {
	return &Warnable{
		Code:     tsconst.HealthWarnableApplyDiskConfig,
		Title:    "无法应用配置",
		Severity: SeverityMedium,
		Text: func(args Args) string {
			return fmt.Sprintf("应用存储在磁盘上的 Tailscale envknob 配置时发生错误：%v", args[ArgError])
		},
	}
})

// warmingUpWarnableDuration is the duration for which the warmingUpWarnable is reported by the backend after the user
// has changed ipnWantRunning to true from false.
const warmingUpWarnableDuration = 5 * time.Second

// warmingUpWarnable is a Warnable that is reported by the backend when it is starting up, for a maximum time of
// warmingUpWarnableDuration. The GUIs use the presence of this Warnable to prevent showing any other warnings until
// the backend is fully started.
var warmingUpWarnable = condRegister(func() *Warnable {
	return &Warnable{
		Code:     tsconst.HealthWarnableWarmingUp,
		Title:    "Tailscale 正在启动",
		Severity: SeverityLow,
		Text:     StaticMessage("Tailscale 正在启动，请稍候。"),
	}
})

// ipForwardingWarnable is a Warnable that warns the user that IP forwarding is disabled
// but subnet routing or exit node functionality is being used.
var ipForwardingWarnable = condRegister(func() *Warnable {
	return &Warnable{
		Code:                "ip-forwarding-off",
		Title:               "IP 转发已关闭",
		Severity:            SeverityMedium,
		MapDebugFlag:        "warn-ip-forwarding-off",
		Text:                StaticMessage("已启用子网路由，但 IP 转发已禁用。请检查你的设备是否已启用 IP 转发。"),
		ImpactsConnectivity: true,
	}
})
