#!/usr/bin/env python3
# 用 Python 标准库 msilib 直接生成 Tailscale 中文定制客户端 MSI 安装包。
# 无需 WiX 工具链 / 管理员权限（生成阶段）。
import msilib, os, uuid
import msilib.schema  # 提供 init_database 的 schema 参数所需对象

SRC = r"E:/github/tailscale-custom/tailscale-zh"          # 三个 exe 所在目录
OUT = os.path.join(SRC, "packaging", "msi", "tailscale.msi")
EXES = ["tailscale.exe", "tailscaled.exe", "systray.exe"]

# 固定 GUID（升级用；换包时保持 UpgradeCode 不变）
PRODUCT_CODE = "{A1B2C3D4-1234-5678-1234-567812345678}"
UPGRADE_CODE = "{B2C3D4E5-1234-5678-1234-567812345679}"

os.makedirs(os.path.dirname(OUT), exist_ok=True)
if os.path.exists(OUT):
    os.remove(OUT)

# schema 参数必须传 msilib.schema（表结构定义）；64 位模板由 init_database 自动按 AMD64 设置
db = msilib.init_database(OUT, msilib.schema, "Tailscale", PRODUCT_CODE, "1.101.0", "Tailscale")

# init_database 已写入 ProductName/ProductCode/ProductVersion/Manufacturer/Language，
# 这里只补它没设的属性（避免 Property 主键冲突）
msilib.add_data(db, "Property", [
    ("UpgradeCode", UPGRADE_CODE),
    ("ALLUSERS", "1"),
    ("ARPNOREPAIR", "1"),
    ("ARPNOMODIFY", "1"),
])

# 目录结构：TARGETDIR -> ProgramFiles64Folder -> Tailscale，以及开始菜单/启动/桌面
msilib.add_data(db, "Directory", [
    ("TARGETDIR", None, "SourceDir"),
    ("ProgramFiles64Folder", "TARGETDIR", "PFiles64"),
    ("TailscaleDir", "ProgramFiles64Folder", "Tailscale"),
    ("ProgramMenuFolder", "TARGETDIR", "Programs"),
    ("TSMenu", "ProgramMenuFolder", "Tailscale"),
    ("StartupFolder", "TARGETDIR", "Startup"),
    ("DesktopFolder", "TARGETDIR", "Desktop"),
])

# 组件：3 个 exe 各一组件 + 4 个快捷方式各一组件
comp_tailscale = "comp_tailscale"
comp_tailscaled = "comp_tailscaled"
comp_systray = "comp_systray"
comp_menu_gui = "comp_menu_gui"
comp_menu_cli = "comp_menu_cli"
comp_startup = "comp_startup"
comp_desktop = "comp_desktop"

msilib.add_data(db, "Component", [
    (comp_tailscale, str(uuid.uuid4()).upper(), "TailscaleDir", 0, None, "tailscale.exe"),
    (comp_tailscaled, str(uuid.uuid4()).upper(), "TailscaleDir", 0, None, "tailscaled.exe"),
    (comp_systray, str(uuid.uuid4()).upper(), "TailscaleDir", 0, None, "systray.exe"),
    (comp_menu_gui, str(uuid.uuid4()).upper(), "TSMenu", 0, None, "TSMenuGui"),
    (comp_menu_cli, str(uuid.uuid4()).upper(), "TSMenu", 0, None, "TSMenuCli"),
    (comp_startup, str(uuid.uuid4()).upper(), "StartupFolder", 0, None, "StartupGui"),
    (comp_desktop, str(uuid.uuid4()).upper(), "DesktopFolder", 0, None, "DesktopGui"),
])

# 文件表（Sequence 从 1 连续）
# 注意：Cabinet.commit 不会回填 FileSize，这里手动写入真实大小，保证磁盘空间计量正确
file_sizes = {exe: os.path.getsize(os.path.join(SRC, exe)) for exe in EXES}
msilib.add_data(db, "File", [
    ("tailscale.exe", comp_tailscale, "tailscale.exe", file_sizes["tailscale.exe"], None, None, 0, 1),
    ("tailscaled.exe", comp_tailscaled, "tailscaled.exe", file_sizes["tailscaled.exe"], None, None, 0, 2),
    ("systray.exe", comp_systray, "systray.exe", file_sizes["systray.exe"], None, None, 0, 3),
])

# 快捷方式：开始菜单 GUI/CLI、启动项 GUI（开机自启）、桌面 GUI
msilib.add_data(db, "Shortcut", [
    ("TSMenuGui", "TSMenu", "Tailscale GUI", comp_menu_gui, "[#systray.exe]", "",
     "Tailscale 图形界面", None, None, None, 1, "TailscaleDir"),
    ("TSMenuCli", "TSMenu", "Tailscale CLI", comp_menu_cli, "[#tailscale.exe]", "",
     "Tailscale 命令行", None, None, None, 1, "TailscaleDir"),
    ("StartupGui", "StartupFolder", "Tailscale GUI", comp_startup, "[#systray.exe]", "",
     "Tailscale 图形界面", None, None, None, 1, "TailscaleDir"),
    ("DesktopGui", "DesktopFolder", "Tailscale", comp_desktop, "[#systray.exe]", "",
     "Tailscale 图形界面", None, None, None, 1, "TailscaleDir"),
])

# 注册 Tailscale 为 Windows 服务（own process / 自动启动 / LocalSystem）
msilib.add_data(db, "ServiceInstall", [
    ("TailscaleService", "Tailscale", "Tailscale", 16, 2, 1, None, None,
     "LocalSystem", None, "", comp_tailscaled, "Tailscale 客户端守护进程"),
])
# Event 0x037(55) = 安装时启动(0x001|0x010) + 卸载时停止(0x002|0x020) + 卸载时删除(0x004|0x020)
# 注意：必须同时带安装/卸载阶段标志位，否则动作不会触发
msilib.add_data(db, "ServiceControl", [
    ("TailscaleServiceCtrl", "Tailscale", 55, "", 1, comp_tailscaled),
])

# 功能与组件关联
msilib.add_data(db, "Feature", [
    ("Complete", None, "Tailscale", "Tailscale 客户端", 1, 1, "TailscaleDir", 0),
])
msilib.add_data(db, "FeatureComponents", [
    ("Complete", comp_tailscale),
    ("Complete", comp_tailscaled),
    ("Complete", comp_systray),
    ("Complete", comp_menu_gui),
    ("Complete", comp_menu_cli),
    ("Complete", comp_startup),
    ("Complete", comp_desktop),
])

# 生成 cabinet 流并回填 Media / File 表
cab = msilib.CAB("tailscale.cab")
for exe in EXES:
    cab.append(os.path.join(SRC, exe), exe, exe)
cab.commit(db)

db.Commit()
print("OK ->", OUT)
