// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

import cx from "classnames"
import React from "react"
import { useAPI } from "src/api"
import * as Control from "src/components/control-components"
import { NodeData } from "src/types"
import Card from "src/ui/card"
import Toggle from "src/ui/toggle"

export default function SSHView({
  readonly,
  node,
}: {
  readonly: boolean
  node: NodeData
}) {
  const api = useAPI()

  return (
    <>
      <h1 className="mb-1">Tailscale SSH 服务</h1>
      <p className="description mb-10">
        在此设备上运行 Tailscale SSH 服务，并允许你 tailnet 中的其他设备通过 SSH 连接进来。{" "}
        <a
          href="https://tailscale.com/kb/1193/tailscale-ssh/"
          className="text-blue-700"
          target="_blank"
          rel="noreferrer"
        >
          了解更多 &rarr;
        </a>
      </p>
      <Card noPadding className="-mx-5 p-5">
        {!readonly ? (
          <label className="flex gap-3 items-center">
            <Toggle
              checked={node.RunningSSHServer}
              onChange={() =>
                api({
                  action: "update-prefs",
                  data: {
                    RunSSHSet: true,
                    RunSSH: !node.RunningSSHServer,
                  },
                })
              }
            />
            <div className="text-black text-sm font-medium leading-tight">
              运行 Tailscale SSH 服务
            </div>
          </label>
        ) : (
          <div className="inline-flex items-center gap-3">
            <span
              className={cx("w-2 h-2 rounded-full", {
                "bg-green-300": node.RunningSSHServer,
                "bg-gray-300": !node.RunningSSHServer,
              })}
            />
            {node.RunningSSHServer ? "运行中" : "未运行"}
          </div>
        )}
      </Card>
      {node.RunningSSHServer && (
        <Control.AdminContainer
          className="text-gray-500 text-sm leading-tight mt-3"
          node={node}
        >
          请记得确认{" "}
          <Control.AdminLink node={node} path="/acls">
            tailnet 策略文件
          </Control.AdminLink>{" "}
          允许其他设备通过 SSH 连接此设备。
        </Control.AdminContainer>
      )}
    </>
  )
}
