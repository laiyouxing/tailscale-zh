// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

import cx from "classnames"
import React, { useCallback, useMemo, useState } from "react"
import ChevronDown from "src/assets/icons/chevron-down.svg?react"
import Eye from "src/assets/icons/eye.svg?react"
import User from "src/assets/icons/user.svg?react"
import { AuthResponse, hasAnyEditCapabilities } from "src/hooks/auth"
import { useTSWebConnected } from "src/hooks/ts-web-connected"
import { NodeData } from "src/types"
import Button from "src/ui/button"
import Popover from "src/ui/popover"
import ProfilePic from "src/ui/profile-pic"
import { assertNever, isHTTPS } from "src/utils/util"

export default function LoginToggle({
  node,
  auth,
  newSession,
}: {
  node: NodeData
  auth: AuthResponse
  newSession: () => Promise<void>
}) {
  const [open, setOpen] = useState<boolean>(false)
  const { tsWebConnected, checkTSWebConnection } = useTSWebConnected(
    auth.serverMode,
    node.IPv4
  )

  return (
    <Popover
      className="p-3 bg-white rounded-lg shadow flex flex-col max-w-[317px]"
      content={
        auth.serverMode === "readonly" ? (
          <ReadonlyModeContent auth={auth} />
        ) : auth.serverMode === "login" ? (
          <LoginModeContent
            auth={auth}
            node={node}
            tsWebConnected={tsWebConnected}
            checkTSWebConnection={checkTSWebConnection}
          />
        ) : auth.serverMode === "manage" ? (
          <ManageModeContent auth={auth} node={node} newSession={newSession} />
        ) : (
          assertNever(auth.serverMode)
        )
      }
      side="bottom"
      align="end"
      open={open}
      onOpenChange={setOpen}
      asChild
    >
      <div>
        {auth.authorized ? (
          <TriggerWhenManaging auth={auth} open={open} setOpen={setOpen} />
        ) : (
          <TriggerWhenReading auth={auth} open={open} setOpen={setOpen} />
        )}
      </div>
    </Popover>
  )
}

/**
 * TriggerWhenManaging is displayed as the trigger for the login popover
 * when the user has an active authorized managment session.
 */
function TriggerWhenManaging({
  auth,
  open,
  setOpen,
}: {
  auth: AuthResponse
  open: boolean
  setOpen: (next: boolean) => void
}) {
  return (
    <div
      className={cx(
        "w-[34px] h-[34px] p-1 rounded-full justify-center items-center inline-flex hover:bg-gray-300",
        {
          "bg-transparent": !open,
          "bg-gray-300": open,
        }
      )}
    >
      <button onClick={() => setOpen(!open)}>
        <ProfilePic size="medium" url={auth.viewerIdentity?.profilePicUrl} />
      </button>
    </div>
  )
}

/**
 * TriggerWhenReading is displayed as the trigger for the login popover
 * when the user is currently in read mode (doesn't have an authorized
 * management session).
 */
function TriggerWhenReading({
  auth,
  open,
  setOpen,
}: {
  auth: AuthResponse
  open: boolean
  setOpen: (next: boolean) => void
}) {
  return (
    <button
      className={cx(
        "pl-3 py-1 bg-gray-700 rounded-full flex justify-start items-center h-[34px]",
        { "pr-1": auth.viewerIdentity, "pr-3": !auth.viewerIdentity }
      )}
      onClick={() => setOpen(!open)}
    >
      <Eye />
      <div className="text-white leading-snug ml-2 mr-1">正在查看</div>
      <ChevronDown className="stroke-white w-[15px] h-[15px]" />
      {auth.viewerIdentity && (
        <ProfilePic
          className="ml-2"
          size="medium"
          url={auth.viewerIdentity.profilePicUrl}
        />
      )}
    </button>
  )
}

/**
 * PopoverContentHeader is the header for the login popover.
 */
function PopoverContentHeader({ auth }: { auth: AuthResponse }) {
  return (
    <div className="text-black text-sm font-medium leading-tight mb-1">
      {auth.authorized ? "管理" : "查看"}
      {auth.viewerIdentity && ` 身份为 ${auth.viewerIdentity.loginName}`}
    </div>
  )
}

/**
 * PopoverContentFooter is the footer for the login popover.
 */
function PopoverContentFooter({ auth }: { auth: AuthResponse }) {
  return auth.viewerIdentity ? (
    <>
      <hr className="my-2" />
      <div className="flex items-center">
        <User className="flex-shrink-0" />
        <p className="text-gray-500 text-xs ml-2">
          我们能识别你，是因为你正从{" "}
          <span className="font-medium">
            {auth.viewerIdentity.nodeName || auth.viewerIdentity.nodeIP}
          </span>{" "}
          访问此页面
        </p>
      </div>
    </>
  ) : null
}

/**
 * ReadonlyModeContent is the body of the login popover when the web
 * client is being run in "readonly" server mode.
 */
function ReadonlyModeContent({ auth }: { auth: AuthResponse }) {
  return (
    <>
      <PopoverContentHeader auth={auth} />
      <p className="text-gray-500 text-xs">
        此 Web 界面正以只读模式运行。{" "}
        <a
          href="https://tailscale.com/s/web-client-read-only"
          className="text-blue-700"
          target="_blank"
          rel="noreferrer"
        >
          了解更多 &rarr;
        </a>
      </p>
      <PopoverContentFooter auth={auth} />
    </>
  )
}

/**
 * LoginModeContent is the body of the login popover when the web
 * client is being run in "login" server mode.
 */
function LoginModeContent({
  node,
  auth,
  tsWebConnected,
  checkTSWebConnection,
}: {
  node: NodeData
  auth: AuthResponse
  tsWebConnected: boolean
  checkTSWebConnection: () => void
}) {
  const https = isHTTPS()
  // We can't run the ts web connection test when the webpage is loaded
  // over HTTPS. So in this case, we default to presenting a login button
  // with some helper text reminding the user to check their connection
  // themselves.
  const hasACLAccess = https || tsWebConnected

  const hasEditCaps = useMemo(() => {
    if (!auth.viewerIdentity) {
      // If not connected to login client over tailscale, we won't know the viewer's
      // identity. So we must assume they may be able to edit something and have the
      // management client handle permissions once the user gets there.
      return true
    }
    return hasAnyEditCapabilities(auth)
  }, [auth])

  const handleLogin = useCallback(() => {
    // Must be connected over Tailscale to log in.
    // Send user to Tailscale IP and start check mode
    const manageURL = `http://${node.IPv4}:5252/?check=now`
    if (window.self !== window.top) {
      // If we're inside an iframe, open management client in new window.
      window.open(manageURL, "_blank")
    } else {
      window.location.href = manageURL
    }
  }, [node.IPv4])

  return (
    <div
      onMouseEnter={
        hasEditCaps && !hasACLAccess ? checkTSWebConnection : undefined
      }
    >
      <PopoverContentHeader auth={auth} />
      {!hasACLAccess || !hasEditCaps ? (
        <>
          <p className="text-gray-500 text-xs">
            {!hasEditCaps ? (
              // ACLs allow access, but user isn't allowed to edit any features,
              // restricted to readonly. No point in sending them over to the
              // tailscaleIP:5252 address.
              <>
                你无权更改此设备，但可以查看其大部分详情。
              </>
            ) : !node.ACLAllowsAnyIncomingTraffic ? (
              // Tailnet ACLs don't allow access to anyone.
              <>
                当前的 tailnet 策略文件不允许连接到此设备。
              </>
            ) : (
              // ACLs don't allow access to this user specifically.
              <>
                无法访问此设备的 Tailscale IP。请确保你已连接到你的 tailnet，
                且你的策略文件允许访问。
              </>
            )}{" "}
            <a
              href="https://tailscale.com/s/web-client-access"
              className="text-blue-700"
              target="_blank"
              rel="noreferrer"
            >
              了解更多 &rarr;
            </a>
          </p>
        </>
      ) : (
        // User can connect to Tailcale IP; sign in when ready.
        <>
          <p className="text-gray-500 text-xs">
            你可以查看此设备的大部分详情。要进行更改，你需要登录。
          </p>
          {https && (
            // we don't know if the user can connect over TS, so
            // provide extra tips in case they have trouble.
            <p className="text-gray-500 text-xs font-semibold pt-2">
              请确保你已连接到你的 tailnet，且你的策略文件允许访问。
            </p>
          )}
          <SignInButton auth={auth} onClick={handleLogin} />
        </>
      )}
      <PopoverContentFooter auth={auth} />
    </div>
  )
}

/**
 * ManageModeContent is the body of the login popover when the web
 * client is being run in "manage" server mode.
 */
function ManageModeContent({
  auth,
  newSession,
}: {
  node: NodeData
  auth: AuthResponse
  newSession: () => void
}) {
  const handleLogin = useCallback(() => {
    if (window.self !== window.top) {
      // If we're inside an iframe, start session in new window.
      let url = new URL(window.location.href)
      url.searchParams.set("check", "now")
      window.open(url, "_blank")
    } else {
      newSession()
    }
  }, [newSession])

  const hasAnyPermissions = useMemo(() => hasAnyEditCapabilities(auth), [auth])

  return (
    <>
      <PopoverContentHeader auth={auth} />
      {!auth.authorized &&
        (hasAnyPermissions ? (
          // User is connected over Tailscale, but needs to complete check mode.
          <>
            <p className="text-gray-500 text-xs">
              要进行更改，请登录以确认你的身份。这一额外步骤有助于保障你的设备安全。
            </p>
            <SignInButton auth={auth} onClick={handleLogin} />
          </>
        ) : (
          // User is connected over tailscale, but doesn't have permission to manage.
          <p className="text-gray-500 text-xs">
            你无权更改此设备，但可以查看其大部分详情。{" "}
            <a
              href="https://tailscale.com/s/web-client-access"
              className="text-blue-700"
              target="_blank"
              rel="noreferrer"
            >
              了解更多 &rarr;
            </a>
          </p>
        ))}
      <PopoverContentFooter auth={auth} />
    </>
  )
}

function SignInButton({
  auth,
  onClick,
}: {
  auth: AuthResponse
  onClick: () => void
}) {
  return (
    <Button
      className={cx("text-center w-full mt-2", {
        "mb-2": auth.viewerIdentity,
      })}
      intent="primary"
      sizeVariant="small"
      onClick={onClick}
    >
      {auth.viewerIdentity ? "登录以确认身份" : "登录"}
    </Button>
  )
}
