#!/usr/bin/env python3
# 用 Python 标准库 msilib 直接生成 Tailscale 中文定制客户端 MSI 安装包。
# 无需 WiX 工具链 / 管理员权限（生成阶段）。
import msilib, os, uuid
import msilib.schema  # 提供 init_database 的 schema 参数所需对象

SRC = r"E:/github/tailscale-custom/tailscale-zh"          # 三个 exe 所在目录
OUT = os.path.join(SRC, "packaging", "msi", "tailscale.msi")
EXES = ["tailscale.exe", "tailscaled.exe", "systray.exe"]

# 固定 GUID（升级用；换包时保持 UpgradeCode 不变）
# 注意：曾因安装失败在 C:\Windows\Installer 留下同 ProductCode 的 1033 缓存，
# msiexec 会复用旧缓存导致模板语言修正不生效，故此处换用新 ProductCode 以强制刷新。
PRODUCT_CODE = "{D4E5F6A7-1234-5678-1234-5678123456CD}"
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

# 语言设为 zh-CN(2052)，与本机 UI 语言一致（InstalledUICulture=2052）。
# Word Count(PID 15) 必须保留 CAB.commit 写入的压缩位 2（=0x0002，
#   表示源文件全部在 cabinet 内）。只有置此位，msiexec 才会从内嵌的
#   #tailscale.cab 流解包；若设为 0，引擎会去外部源路径
#   (...\msi\PFiles64\Tailscale\...) 找文件，导致“系统错误 3”+1920。
# init_database 会同时在 (a) 模板摘要 PID_TEMPLATE 和 (b) Property 表两处
# 写死 1033；语言检查读 (b)，所以两处都要改成 2052。
# 注意：ProductLanguage 已被 init_database 写入，不能再用 add_data（会主键重复报 2259），
# 必须用 UPDATE 修改已有行。
# 关键认知：「程序和功能」无 ARP 条目的真正原因是 InstallExecuteSequence 里
#   漏写了 RegisterProduct 这个标准动作！它负责把 Uninstall 键 +
#   InstallProperties 写进注册表；只写 PublishFeatures/PublishProduct 是不够的。
#   （之前把锅甩给 ProductLanguage=0、WordCount=2 都是误判——它们都正常。）
_si = db.GetSummaryInformation(3)   # 3 = 读写模式
_si.SetProperty(7, "x64;2052")    # (a) 模板摘要（zh-CN）
_si.SetProperty(15, 2)            # (c) Word Count：压缩位（内嵌 cabinet 解包所需）
_si.Persist()
_v = db.OpenView("UPDATE `Property` SET `Value`='2052' WHERE `Property`='ProductLanguage'")
_v.Execute(None); _v.Close()            # (b) Property 表的 ProductLanguage（zh-CN）

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
    # 仅含快捷方式的组件不能有文件型 KeyPath（否则 CostFinalize 报 2715：
    # Windows 会把 KeyPath 当文件键去 File 表查而失败）。KeyPath 置空，
    # 由下面的 CreateFolder 行让对应目录成为该组件的有效 KeyPath。
    (comp_menu_gui, str(uuid.uuid4()).upper(), "TSMenu", 0, None, None),
    (comp_menu_cli, str(uuid.uuid4()).upper(), "TSMenu", 0, None, None),
    (comp_startup, str(uuid.uuid4()).upper(), "StartupFolder", 0, None, None),
    (comp_desktop, str(uuid.uuid4()).upper(), "DesktopFolder", 0, None, None),
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

# 仅含快捷方式的组件需要一个有效 KeyPath：CreateFolder 让该目录成为组件 KeyPath。
# 否则 KeyPath 为空且组件无文件/注册表，Windows 在 CostFinalize 阶段无法定位组件状态。
msilib.add_data(db, "CreateFolder", [
    ("TSMenu", comp_menu_gui),
    ("TSMenu", comp_menu_cli),
    ("StartupFolder", comp_startup),
    ("DesktopFolder", comp_desktop),
])

# 注册为 Windows 服务（own process / 自动启动 / LocalSystem）
# 服务名用 TailscaleZH（而非 Tailscale）：本机残留了一个 Stopped 的“Tailscale”
# 孤儿服务（来自早期调试安装，无 ARP 且 sc.exe 删除被策略拦截），若用同名，
# InstallServices 的 CreateService 会因重名直接报 1923。改用唯一名即可彻底避开冲突。
msilib.add_data(db, "ServiceInstall", [
    ("TailscaleZHService", "TailscaleZH", "Tailscale 自定义客户端", 16, 2, 1, None, None,
     "LocalSystem", None, "", comp_tailscaled, "Tailscale 客户端守护进程"),
])
# Event 0x063(99) = 安装时 启动(0x001)+停止旧实例(0x002)
#                  + 卸载时 停止(0x020)+删除(0x040)
msilib.add_data(db, "ServiceControl", [
    ("TailscaleZHServiceCtrl", "TailscaleZH", 99, "", 1, comp_tailscaled),
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

# 标准安装执行序列。
# 关键：本环境的 msilib.init_database 不会自动填充 InstallExecuteSequence，
# 若缺省，引擎无动作可执行，msiexec 会“秒回成功”却什么都没装。
# 顺序：Costing -> 卸载清理 -> InstallFiles/CreateShortcuts ->
#        InstallServices/StartServices/RegisterServices -> Publish* -> InstallFinalize
msilib.add_data(db, "InstallExecuteSequence", [
    ("CostInitialize", None, 800),
    ("FileCost", None, 900),
    ("CostFinalize", None, 1000),
    ("InstallValidate", None, 1400),
    ("InstallInitialize", None, 1500),
    ("ProcessComponents", None, 1600),
    ("UnpublishComponents", None, 1700),
    ("RegisterComponents", None, 1800),  # 写组件状态（Installer\UserData\...\Components）；漏写则卸载/重装时组件被视为 Absent，什么都不会做
    ("StopServices", None, 1900),       # 卸载/重装时停止服务（ServiceControl 0x002）
    ("DeleteServices", None, 2000),      # 卸载时删除服务（ServiceControl 0x004）
    ("RemoveFiles", None, 3500),
    ("RemoveFolders", None, 3600),
    ("CreateFolders", None, 3700),
    ("InstallFiles", None, 4000),       # 从 cabinet 解包三个 exe
    ("CreateShortcuts", None, 4500),   # 开始菜单/桌面/启动项快捷方式
    ("InstallServices", None, 5800),    # 注册 tailscaled 服务（ServiceInstall）
    ("StartServices", None, 5900),     # 安装时启动服务（ServiceControl 0x001）
    ("RegisterServices", None, 6000),
    ("RegisterProduct", None, 6200),  # 写 Uninstall 键 + InstallProperties（ARP 可见性的关键，漏写则“程序和功能”无条目）
    ("PublishFeatures", None, 6300),
    ("PublishProduct", None, 6400),     # 写 ARP 条目
    ("InstallFinalize", None, 6600),
])

# 生成 cabinet 流并回填 Media / File 表
cab = msilib.CAB("tailscale.cab")
for exe in EXES:
    cab.append(os.path.join(SRC, exe), exe, exe)
cab.commit(db)

db.Commit()
print("OK ->", OUT)
