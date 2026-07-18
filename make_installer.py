#!/usr/bin/env python3
# 单文件 exe 安装包（PyInstaller --onefile 打包，--add-data 内嵌 3 个客户端 exe + 图标）。
# 运行后弹窗让用户选择安装目录（默认 %LOCALAPPDATA%\Tailscale\），
# 把客户端与图标释放到该目录，创建预置 headscale 服务器（LOGIN_SERVER）的
# 桌面/开始菜单/启动快捷方式（均带 tailscale 图标），
# 并以用户态（--tun=userspace-networking）启动守护进程。
# 不写 Program Files、不注册系统服务、不需要管理员 —— 绕开本机 Windows Installer / 服务策略限制。
import os, sys, shutil, subprocess, ctypes, struct, winreg

LOGIN_SERVER = "https://laiyouxing.top:50011/"
EXES = ["tailscale.exe", "tailscaled.exe", "systray.exe"]
ICO = "tailscale.ico"
SOCKET = r"\\.\pipe\tailscale"  # Windows 命名管道（两个反斜杠开头）


def resource_path(rel):
    # PyInstaller 打包后数据文件在 sys._MEIPASS；未打包时在同目录
    base = getattr(sys, "_MEIPASS", os.path.dirname(os.path.abspath(__file__)))
    return os.path.join(base, rel)


def mbox(msg, title="Tailscale 安装"):
    try:
        ctypes.windll.user32.MessageBoxW(0, str(msg), title, 0x40)
    except Exception:
        print(msg)


def choose_dir(default):
    # 可选：环境变量指定（静默/自动化安装，也便于无界面测试）
    env = os.environ.get("TAILSCALE_INSTALL_DIR")
    if env:
        return env
    # 否则弹窗让用户选目录（FolderBrowserDialog，需 STA 线程）
    ps = (
        "Add-Type -AssemblyName System.Windows.Forms; "
        "$d = New-Object System.Windows.Forms.FolderBrowserDialog; "
        "$d.Description = '选择 Tailscale 安装目录（将释放 3 个程序）'; "
        "$d.SelectedPath = '" + default + "'; "
        "$d.ShowNewFolderButton = $true; "
        "if ($d.ShowDialog() -eq 'OK') { $d.SelectedPath }"
    )
    try:
        out = subprocess.run(["powershell", "-STA", "-NoProfile", "-Command", ps],
                             capture_output=True, text=True, timeout=180)
        p = out.stdout.strip()
        return p if p else default
    except Exception:
        return default


def _u16(s):
    return s.encode("utf-16-le") + b"\x00\x00"

def _str_item(s):
    # StringData 项：2 字节长度(UTF-16 字符数) + UTF-16LE 字符串 + 2 字节空终止
    data = s.encode("utf-16-le")
    return struct.pack("<H", len(data) // 2) + data + b"\x00\x00"

def _build_linkinfo(target):
    # Shell Link 的 LinkInfo：用 VolumeID + LocalBasePath 存放目标绝对路径（UTF-16LE）
    tgt = _u16(target)
    # VolumeID（含空 Unicode 卷标）
    vol = (struct.pack("<I", 3)        # DriveType = DRIVE_FIXED
           + struct.pack("<I", 0)      # DriveSerialNumber
           + struct.pack("<I", 0x14)   # VolumeLabelOffset = 0x14（表示用 Unicode 卷标）
           + struct.pack("<I", 0x14)   # VolumeLabelOffsetUnicode
           + b"\x00\x00")              # 空 Unicode 卷标
    vol = struct.pack("<I", len(vol)) + vol
    hdr = 28
    vol_off = hdr
    lbp_off = hdr + len(vol)
    cps_off = hdr + len(vol) + len(tgt)
    li = (struct.pack("<I", 0)         # LinkInfoSize（占位，稍后回填）
          + struct.pack("<I", hdr)     # LinkInfoHeaderSize
          + struct.pack("<I", 0x1)     # LinkInfoFlags = VolumeIDAndLocalBasePath
          + struct.pack("<I", vol_off)
          + struct.pack("<I", lbp_off)
          + struct.pack("<I", 0)       # CommonNetworkRelativeLinkOffset
          + struct.pack("<I", cps_off)
          + vol + tgt + b"\x00\x00")   # CommonPathSuffix（空）
    return struct.pack("<I", len(li)) + li[4:]

def _build_header(flags, show_cmd):
    h = struct.pack("<I", 0x4C)                                  # HeaderSize
    h += bytes.fromhex("0114020000000000C000000000000046")        # LinkCLSID
    h += struct.pack("<I", flags)
    h += struct.pack("<I", 0)                                     # FileFlags
    h += struct.pack("<Q", 0)                                     # CreationTime
    h += struct.pack("<Q", 0)                                     # AccessTime
    h += struct.pack("<Q", 0)                                     # WriteTime
    h += struct.pack("<I", 0)                                     # FileSize
    h += struct.pack("<I", 0)                                     # IconIndex
    h += struct.pack("<I", show_cmd)                               # ShowCommand
    h += struct.pack("<H", 0)                                     # HotKey
    h += struct.pack("<H", 0)                                     # Reserved1
    h += struct.pack("<I", 0)                                     # Reserved2
    h += struct.pack("<I", 0)                                     # Reserved3
    return h

def make_lnk(lnk_path, target, args="", workdir="", icon="", show_cmd=1):
    # 纯 Python 生成 .lnk（Shell Link 二进制），不依赖 powershell / WScript.Shell COM，
    # 在 WDAC/AppLocker 锁定环境下也能工作。
    # flags: HasLinkInfo(0x2) | HasWorkingDir(0x10) | HasIconLocation(0x40) | IsUnicode(0x80)
    flags = 0x02 | 0x10 | 0x40 | 0x80
    if args:
        flags |= 0x20  # HasArguments
    body = _build_linkinfo(target)
    if workdir:
        body += _str_item(workdir)
    if args:
        body += _str_item(args)
    if icon:
        body += _str_item(icon + ",0")  # ICON_LOCATION 形如 "path.ico,0"
    data = _build_header(flags, show_cmd) + body
    with open(lnk_path, "wb") as f:
        f.write(data)


def add_to_path(install_dir):
    # 把安装目录加入用户级 PATH（HKCU\Environment\Path），无需管理员；
    # 之后命令行即可直接运行 tailscale / tailscaled，无需 cd 进目录。
    key = winreg.OpenKey(winreg.HKEY_CURRENT_USER, "Environment",
                          0, winreg.KEY_READ | winreg.KEY_WRITE)
    try:
        cur, val_type = winreg.QueryValueEx(key, "Path")
    except FileNotFoundError:
        cur, val_type = "", winreg.REG_SZ
    parts = [p for p in cur.split(";") if p.strip()]
    if install_dir not in parts:
        parts.append(install_dir)
        winreg.SetValueEx(key, "Path", 0, val_type, ";".join(parts))
        # 广播 WM_SETTINGCHANGE，让系统/新进程刷新环境变量（当前进程不立即生效）
        HWND_BROADCAST = 0xFFFF
        WM_SETTINGCHANGE = 0x1A
        SMTO_ABORTIFHUNG = 0x2
        ctypes.windll.user32.SendMessageTimeoutW(
            HWND_BROADCAST, WM_SETTINGCHANGE, 0,
            "Environment", SMTO_ABORTIFHUNG, 1000, None)
    winreg.CloseKey(key)


def main():
    install_dir = choose_dir(os.path.join(
        os.environ.get("LOCALAPPDATA", os.path.expanduser("~\\AppData\\Local")), "Tailscale"))

    os.makedirs(install_dir, exist_ok=True)
    for exe in EXES:
        shutil.copy2(resource_path(exe), os.path.join(install_dir, exe))
    # 释放图标，供快捷方式引用
    shutil.copy2(resource_path(ICO), os.path.join(install_dir, ICO))
    ico_path = os.path.join(install_dir, ICO)

    desk = os.path.join(os.environ["USERPROFILE"], "Desktop")
    start = os.path.join(os.environ["APPDATA"], "Microsoft", "Windows", "Start Menu", "Programs")
    startup = os.path.join(start, "Startup")
    os.makedirs(start, exist_ok=True)
    os.makedirs(startup, exist_ok=True)

    # 连接服务器（预置 login-server）
    conn_args = "up --login-server=" + LOGIN_SERVER + " --accept-routes"
    make_lnk(os.path.join(desk, "Tailscale 连接.lnk"),
             os.path.join(install_dir, "tailscale.exe"), conn_args, install_dir, ico_path)
    make_lnk(os.path.join(start, "Tailscale 连接.lnk"),
             os.path.join(install_dir, "tailscale.exe"), conn_args, install_dir, ico_path)
    # GUI
    make_lnk(os.path.join(desk, "Tailscale GUI.lnk"),
             os.path.join(install_dir, "systray.exe"), "", install_dir, ico_path)
    # 开机自启守护进程（用户态，放启动文件夹；不注册系统服务）
    # show_cmd=0(SW_HIDE)：tailscaled 是控制台程序，开机自启时隐藏其窗口，避免黑框
    svc_args = "--tun=userspace-networking --state=" + os.path.join(install_dir, "state") + " --socket=" + SOCKET
    make_lnk(os.path.join(startup, "Tailscale.lnk"),
             os.path.join(install_dir, "tailscaled.exe"), svc_args, install_dir, ico_path, show_cmd=0)

    # 启动守护进程（无窗口运行，日志写 tailscaled.log；父退出后继续）
    log_path = os.path.join(install_dir, "tailscaled.log")
    with open(log_path, "w") as logf:
        subprocess.Popen(
            [os.path.join(install_dir, "tailscaled.exe"),
             "--tun=userspace-networking",
             "--state=" + os.path.join(install_dir, "state"),
             "--socket=" + SOCKET],
            stdout=logf, stderr=subprocess.STDOUT,
            creationflags=subprocess.CREATE_NO_WINDOW,
        )

    add_to_path(install_dir)

    mbox("已安装到：\n" + install_dir + "\n\n"
          "• 双击桌面「Tailscale 连接」即可连 " + LOGIN_SERVER + "\n"
          "• 需先在 headscale 控制台授权该设备\n"
          "• 守护进程已以用户态启动")


if __name__ == "__main__":
    main()
