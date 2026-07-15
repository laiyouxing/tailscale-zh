// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

import React from "react"
import CheckCircleIcon from "src/assets/icons/check-circle.svg?react"
import XCircleIcon from "src/assets/icons/x-circle.svg?react"
import { ChangelogText } from "src/components/update-available"
import { UpdateState, useInstallUpdate } from "src/hooks/self-update"
import { VersionInfo } from "src/types"
import Button from "src/ui/button"
import Spinner from "src/ui/spinner"
import { useLocation } from "wouter"

/**
 * UpdatingView is rendered when the user initiates a Tailscale update, and
 * the update is in-progress, failed, or completed.
 */
export function UpdatingView({
  versionInfo,
  currentVersion,
}: {
  versionInfo?: VersionInfo
  currentVersion: string
}) {
  const [, setLocation] = useLocation()
  const { updateState, updateLog } = useInstallUpdate(
    currentVersion,
    versionInfo
  )
  return (
    <>
      <div className="flex-1 flex flex-col justify-center items-center text-center mt-56">
        {updateState === UpdateState.InProgress ? (
          <>
            <Spinner size="sm" className="text-gray-400" />
            <h1 className="text-2xl m-3">更新进行中</h1>
            <p className="text-gray-400">
              更新过程通常只需几分钟。完成后，系统会要求你重新登录。
            </p>
          </>
        ) : updateState === UpdateState.Complete ? (
          <>
            <CheckCircleIcon />
            <h1 className="text-2xl m-3">更新完成！</h1>
            <p className="text-gray-400">
              你已将 Tailscale 更新
              {versionInfo && versionInfo.LatestVersion
                ? ` 到 ${versionInfo.LatestVersion}`
                : null}
              。<ChangelogText version={versionInfo?.LatestVersion} />
            </p>
            <Button
              className="m-3"
              sizeVariant="small"
              onClick={() => setLocation("/")}
            >
              登录以访问
            </Button>
          </>
        ) : updateState === UpdateState.UpToDate ? (
          <>
            <CheckCircleIcon />
            <h1 className="text-2xl m-3">已是最新！</h1>
            <p className="text-gray-400">
              你已经在运行 Tailscale {currentVersion}，这是当前可用的最新版本。
            </p>
            <Button
              className="m-3"
              sizeVariant="small"
              onClick={() => setLocation("/")}
            >
              返回
            </Button>
          </>
        ) : (
          /* TODO(naman,sonia): Figure out the body copy and design for this view. */
          <>
            <XCircleIcon />
            <h1 className="text-2xl m-3">更新失败</h1>
            <p className="text-gray-400">
              更新
              {versionInfo && versionInfo.LatestVersion
                ? ` 到 ${versionInfo.LatestVersion}`
                : null}{" "}
              失败。
            </p>
            <Button
              className="m-3"
              sizeVariant="small"
              onClick={() => setLocation("/")}
            >
              返回
            </Button>
          </>
        )}
        <pre className="h-64 overflow-scroll m-3">
          <code>{updateLog}</code>
        </pre>
      </div>
    </>
  )
}
