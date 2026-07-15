// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

import React from "react"
import { useAPI } from "src/api"
import TailscaleIcon from "src/assets/icons/tailscale-icon.svg?react"
import { NodeData } from "src/types"
import Button from "src/ui/button"

/**
 * LoginView is rendered when the client is not authenticated
 * to a tailnet.
 */
export default function LoginView({ data }: { data: NodeData }) {
  const api = useAPI()

  return (
    <div className="mb-8 py-6 px-8 bg-white rounded-md shadow-2xl">
      <TailscaleIcon className="my-2 mb-8" />
      {data.Status === "Stopped" ? (
        <>
          <div className="mb-6">
            <h3 className="text-3xl font-semibold mb-3">连接</h3>
            <p className="text-gray-700">
              你的设备已断开与 Tailscale 的连接。
            </p>
          </div>
          <Button
            onClick={() => api({ action: "up", data: {} })}
            className="w-full mb-4"
            intent="primary"
          >
            连接到 Tailscale
          </Button>
        </>
      ) : data.IPv4 ? (
        <>
          <div className="mb-6">
            <p className="text-gray-700">
              你设备的密钥已过期。请重新登录以重新验证此设备，或{" "}
              <a
                href="https://tailscale.com/kb/1028/key-expiry"
                className="link"
                target="_blank"
                rel="noreferrer"
              >
                了解更多
              </a>
              。
            </p>
          </div>
          <Button
            onClick={() =>
              api({ action: "up", data: { Reauthenticate: true } })
            }
            className="w-full mb-4"
            intent="primary"
          >
            重新验证
          </Button>
        </>
      ) : (
        <>
          <div className="mb-6">
            <h3 className="text-3xl font-semibold mb-3">登录</h3>
            <p className="text-gray-700">
              登录到你的 Tailscale 网络以开始使用。
              或者，在{" "}
              <a
                href="https://tailscale.com/"
                className="link"
                target="_blank"
                rel="noreferrer"
              >
                tailscale.com
              </a>
              了解更多。
            </p>
          </div>
          <Button
            onClick={() =>
              api({
                action: "up",
                data: {
                  Reauthenticate: true,
                },
              })
            }
            className="w-full mb-4"
            intent="primary"
          >
            登录
          </Button>
        </>
      )}
    </div>
  )
}
