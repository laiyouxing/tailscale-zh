// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

import cx from "classnames"
import React, { useCallback, useMemo, useState } from "react"
import { useAPI } from "src/api"
import CheckCircle from "src/assets/icons/check-circle.svg?react"
import Clock from "src/assets/icons/clock.svg?react"
import Plus from "src/assets/icons/plus.svg?react"
import * as Control from "src/components/control-components"
import { NodeData } from "src/types"
import Button from "src/ui/button"
import Card from "src/ui/card"
import Dialog from "src/ui/dialog"
import EmptyState from "src/ui/empty-state"
import Input from "src/ui/input"

export default function SubnetRouterView({
  readonly,
  node,
}: {
  readonly: boolean
  node: NodeData
}) {
  const api = useAPI()

  const [advertisedRoutes, hasRoutes, hasUnapprovedRoutes] = useMemo(() => {
    const routes = node.AdvertisedRoutes || []
    return [routes, routes.length > 0, routes.find((r) => !r.Approved)]
  }, [node.AdvertisedRoutes])

  const [inputOpen, setInputOpen] = useState<boolean>(
    advertisedRoutes.length === 0 && !readonly
  )
  const [inputText, setInputText] = useState<string>("")
  const [postError, setPostError] = useState<string>()

  const resetInput = useCallback(() => {
    setInputText("")
    setPostError("")
    setInputOpen(false)
  }, [])

  return (
    <>
      <h1 className="mb-1">子网路由器</h1>
      <p className="description mb-5">
        在不安装 Tailscale 的情况下，将设备添加到你的 tailnet 中。{" "}
        <a
          href="https://tailscale.com/kb/1019/subnets/"
          className="text-blue-700"
          target="_blank"
          rel="noreferrer"
        >
          了解更多 &rarr;
        </a>
      </p>
      {!readonly &&
        (inputOpen ? (
          <Card noPadding className="-mx-5 p-5 !border-0 shadow-popover">
            <p className="font-medium leading-snug mb-3">
              发布新路由
            </p>
            <Input
              type="text"
              className="text-sm"
              placeholder="192.168.0.0/24"
              value={inputText}
              onChange={(e) => {
                setPostError("")
                setInputText(e.target.value)
              }}
            />
            <p
              className={cx("my-2 h-6 text-sm leading-tight", {
                "text-gray-500": !postError,
                "text-red-400": postError,
              })}
            >
              {postError ||
                "使用逗号分隔的列表可添加多条路由。"}
            </p>
            <div className="flex gap-3">
              <Button
                intent="primary"
                onClick={() =>
                  api({
                    action: "update-routes",
                    data: [
                      ...advertisedRoutes,
                      ...inputText
                        .split(",")
                        .map((r) => ({ Route: r, Approved: false })),
                    ],
                  })
                    .then(resetInput)
                    .catch((err: Error) => setPostError(err.message))
                }
                disabled={!inputText || postError !== ""}
              >
                发布{hasRoutes && "新"}路由
              </Button>
              {hasRoutes && <Button onClick={resetInput}>取消</Button>}
            </div>
          </Card>
        ) : (
          <Button
            intent="primary"
            prefixIcon={<Plus />}
            onClick={() => setInputOpen(true)}
          >
            发布新路由
          </Button>
        ))}
      <div className="-mx-5 mt-10">
        {hasRoutes ? (
          <>
            <Card noPadding className="px-5 py-3">
              {advertisedRoutes.map((r) => (
                <div
                  className="flex justify-between items-center pb-2.5 mb-2.5 border-b border-b-gray-200 last:pb-0 last:mb-0 last:border-b-0"
                  key={r.Route}
                >
                  <div className="text-gray-800 leading-snug">{r.Route}</div>
                  <div className="flex items-center gap-3">
                    <div className="flex items-center gap-1.5">
                      {r.Approved ? (
                        <CheckCircle className="w-4 h-4" />
                      ) : (
                        <Clock className="w-4 h-4" />
                      )}
                      {r.Approved ? (
                        <div className="text-green-500 text-sm leading-tight">
                          已批准
                        </div>
                      ) : (
                        <div className="text-gray-500 text-sm leading-tight">
                          待审批
                        </div>
                      )}
                    </div>
                    {!readonly && (
                      <StopAdvertisingDialog
                        onSubmit={() =>
                          api({
                            action: "update-routes",
                            data: advertisedRoutes.filter(
                              (it) => it.Route !== r.Route
                            ),
                          })
                        }
                      />
                    )}
                  </div>
                </div>
              ))}
            </Card>
            {hasUnapprovedRoutes && (
              <Control.AdminContainer
                className="mt-3 w-full text-center text-gray-500 text-sm leading-tight"
                node={node}
              >
                要批准路由，请在管理控制台中打开{" "}
                <Control.AdminLink node={node} path={`/machines/${node.IPv4}`}>
                  此设备的路由设置
                </Control.AdminLink>
                。
              </Control.AdminContainer>
            )}
          </>
        ) : (
          <Card empty>
            <EmptyState description="未发布任何路由" />
          </Card>
        )}
      </div>
    </>
  )
}

function StopAdvertisingDialog({ onSubmit }: { onSubmit: () => void }) {
  return (
    <Dialog
      className="max-w-md"
      title="停止发布路由"
      trigger={<Button sizeVariant="small">停止发布…</Button>}
    >
      <Dialog.Form
        cancelButton
        submitButton="停止发布"
        destructive
        onSubmit={onSubmit}
      >
        此路由上设备之间的任何活动连接都将被中断。
      </Dialog.Form>
    </Dialog>
  )
}
