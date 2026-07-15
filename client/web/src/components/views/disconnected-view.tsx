// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

import React from "react"
import TailscaleIcon from "src/assets/icons/tailscale-icon.svg?react"

/**
 * DisconnectedView is rendered after node logout.
 */
export default function DisconnectedView() {
  return (
    <>
      <TailscaleIcon className="mx-auto" />
      <p className="mt-12 text-center text-text-muted">
        你已退出此设备的登录。要重新连接，你需要从 Tailscale 应用或
        Tailscale 命令行界面重新验证此设备。
      </p>
    </>
  )
}
