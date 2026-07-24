; iron-link Windows installer (Inno Setup).
;
; Bundles the two halves of the desktop app:
;   - iron-link-daemon.exe  — the elevated Go daemon (embeds sing-box + xray
;     + wintun; needs Administrator for TUN). Installed at the app root.
;   - the Flutter client      — the unprivileged GUI, a thin front-end over the
;     daemon's named-pipe IPC. Installed under app\.
;
; The daemon is registered as an auto-start WINDOWS SERVICE (`install` below),
; so it runs in the background with no console window and no per-launch UAC, and
; survives reboots. It runs as LocalSystem (TUN needs Administrator). The config
; dir wrinkle — a LocalSystem service would resolve SYSTEM's %APPDATA%, not the
; user's — is solved by `install` baking the INSTALLING user's config dir into
; the service environment (IRON_LINK_CONFIG_DIR), so the service shares
; profiles/state with the unprivileged client. The client connects to the same
; named pipe (\\.\pipe\iron-link, DACL grants the interactive user) as always.
;
; Built by .github/workflows/build.yml on the windows-latest runner; the version
; and the two source paths arrive as /D defines from ISCC. The defaults below
; only serve a manual local compile from this directory.

#ifndef AppVersion
  #define AppVersion "0.0.0-dev"
#endif
#ifndef DaemonExe
  #define DaemonExe "..\..\daemon-go\bin\iron-link-daemon.exe"
#endif
#ifndef FlutterDir
  #define FlutterDir "..\..\client-flutter\build\windows\x64\runner\Release"
#endif
#ifndef OutputDir
  #define OutputDir "..\..\dist"
#endif
#ifndef OutputBase
  #define OutputBase "iron-link-setup"
#endif

[Setup]
AppId={{B2A6F4E1-9C3D-4A57-8E2B-6F1D0C9A4E73}
AppName=iron-link
AppVersion={#AppVersion}
AppPublisher=iron-link
DefaultDirName={autopf}\iron-link
DefaultGroupName=iron-link
DisableProgramGroupPage=yes
UninstallDisplayIcon={app}\app\iron_link_flutter.exe
OutputDir={#OutputDir}
OutputBaseFilename={#OutputBase}
Compression=lzma2
SolidCompression=yes
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
; The installer elevates to write Program Files and register the service.
PrivilegesRequired=admin
WizardStyle=modern

[Files]
; The daemon is self-contained (cores + wintun embedded) — just the one exe.
Source: "{#DaemonExe}"; DestDir: "{app}"; Flags: ignoreversion
; The Flutter bundle: exe + flutter_windows.dll + plugin DLLs + data\.
Source: "{#FlutterDir}\*"; DestDir: "{app}\app"; Flags: ignoreversion recursesubdirs createallsubdirs

[Tasks]
Name: "desktopicon"; Description: "Create a &desktop shortcut"; GroupDescription: "Additional shortcuts:"

[Icons]
Name: "{group}\iron-link"; Filename: "{app}\app\iron_link_flutter.exe"
Name: "{group}\Uninstall iron-link"; Filename: "{uninstallexe}"
Name: "{autodesktop}\iron-link"; Filename: "{app}\app\iron_link_flutter.exe"; Tasks: desktopicon

[Run]
; Register the daemon as an auto-start Windows service and start it now. The
; service runs as LocalSystem (TUN needs Administrator) with no console window;
; `install` (run here in the user's elevated context) bakes THIS user's config
; dir into the service environment so it shares profiles/state with the client.
Filename: "{app}\iron-link-daemon.exe"; Parameters: "install"; StatusMsg: "Registering the iron-link service..."; Flags: runhidden
Filename: "{app}\iron-link-daemon.exe"; Parameters: "start"; StatusMsg: "Starting the iron-link service..."; Flags: runhidden
; Launch the app (unprivileged) — it connects to the service over the pipe.
Filename: "{app}\app\iron_link_flutter.exe"; Description: "Launch iron-link"; Flags: postinstall nowait skipifsilent

[UninstallRun]
; Stop + deregister the service before its files are deleted.
Filename: "{app}\iron-link-daemon.exe"; Parameters: "stop"; Flags: runhidden; RunOnceId: "StopSvc"
Filename: "{app}\iron-link-daemon.exe"; Parameters: "uninstall"; Flags: runhidden; RunOnceId: "DelSvc"

[Code]
function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  rc: Integer;
begin
  // Upgrade path: stop a previous service so its (locked) exe can be
  // overwritten by [Files]. No-op on a first install (the exe isn't there yet).
  Exec(ExpandConstant('{app}\iron-link-daemon.exe'), 'stop', '', SW_HIDE,
    ewWaitUntilTerminated, rc);
  // Also close a running client. Otherwise its locked iron_link_flutter.exe
  // can't be overwritten and the upgrade silently keeps the OLD binary — old
  // UI, old embedded icon. The client is a thin front-end (all state lives in
  // the daemon), so a force-close loses nothing.
  Exec(ExpandConstant('{sys}\taskkill.exe'), '/F /IM iron_link_flutter.exe',
    '', SW_HIDE, ewWaitUntilTerminated, rc);
  Result := '';
end;
