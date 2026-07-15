// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

import { assertNever } from "src/utils/util"

export type NodeData = {
  Profile: UserProfile
  Status: NodeState
  DeviceName: string
  OS: string
  IPv4: string
  IPv6: string
  ID: string
  KeyExpiry: string
  KeyExpired: boolean
  UsingExitNode?: ExitNode
  AdvertisingExitNode: boolean
  AdvertisingExitNodeApproved: boolean
  AdvertisedRoutes?: SubnetRoute[]
  TUNMode: boolean
  IsSynology: boolean
  DSMVersion: number
  IsUnraid: boolean
  UnraidToken: string
  IPNVersion: string
  ClientVersion?: VersionInfo
  URLPrefix: string
  DomainName: string
  TailnetName: string
  IsTagged: boolean
  Tags: string[]
  RunningSSHServer: boolean
  ControlAdminURL: string
  LicensesURL: string
  Features: { [key in Feature]: boolean } // value is true if given feature is available on this client
  ACLAllowsAnyIncomingTraffic: boolean
}

export type NodeState =
  | "NoState"
  | "NeedsLogin"
  | "NeedsMachineAuth"
  | "Stopped"
  | "Starting"
  | "Running"

export type UserProfile = {
  LoginName: string
  DisplayName: string
  ProfilePicURL: string
}

export type SubnetRoute = {
  Route: string
  Approved: boolean
}

export type ExitNode = {
  ID: string
  Name: string
  Location?: ExitNodeLocation
  Online?: boolean
}

export type ExitNodeLocation = {
  Country: string
  CountryCode: CountryCode
  City: string
  CityCode: CityCode
  Priority: number
}

export type CountryCode = string
export type CityCode = string

export type ExitNodeGroup = {
  id: string
  name?: string
  nodes: ExitNode[]
}

export type Feature =
  | "advertise-exit-node"
  | "advertise-routes"
  | "use-exit-node"
  | "ssh"
  | "auto-update"

export const featureDescription = (f: Feature) => {
  switch (f) {
    case "advertise-exit-node":
      return "作为出口节点发布"
    case "advertise-routes":
      return "发布子网路由"
    case "use-exit-node":
      return "使用出口节点"
    case "ssh":
      return "运行 Tailscale SSH 服务"
    case "auto-update":
      return "自动更新客户端版本"
    default:
      assertNever(f)
  }
}

/**
 * VersionInfo type is deserialized from tailcfg.ClientVersion,
 * so it should not include fields not included in that type.
 */
export type VersionInfo = {
  RunningLatest: boolean
  LatestVersion?: string
}
