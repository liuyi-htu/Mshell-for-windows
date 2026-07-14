#ifndef MyArchitectureSuffix
  #define MyArchitectureSuffix "x64"
#endif

#define MyAppName "Mshell"
#define MyAppVersion "1.0.2"
#define MyAppPublisher "Project"
#define MshellExeName "Mshell.exe"
#define MsftpExeName "Msftp.exe"
#define MshellIcon "..\Mshell\assets\app-icon.ico"
#define MsftpIcon "..\Msftp\assets\app-icon.ico"

[Setup]
AppId={{C991B69D-07D3-4D47-94A9-FA2E6328D23A}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher={#MyAppPublisher}
DefaultDirName=D:\Program Files\{#MyAppName}
DefaultGroupName={#MyAppName}
AllowNoIcons=yes
OutputDir=Output
OutputBaseFilename=Mshell-Setup-{#MyArchitectureSuffix}
Compression=lzma
SolidCompression=yes
WizardStyle=modern
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
PrivilegesRequired=lowest
UninstallDisplayName={#MyAppName}
UninstallDisplayIcon={app}\Mshell.exe
SetupIconFile={#MshellIcon}
CloseApplications=yes
CloseApplicationsFilter={#MshellExeName},{#MsftpExeName}
RestartIfNeededByRun=no

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[CustomMessages]
english.CreateMshellDesktopShortcut=Create an Mshell desktop shortcut
english.CreateMsftpDesktopShortcut=Create an Msftp desktop shortcut
english.MshellShortcutName=Mshell
english.MsftpShortcutName=Msftp

[Tasks]
Name: "desktopicon_mshell"; Description: "{cm:CreateMshellDesktopShortcut}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: checkedonce
Name: "desktopicon_msftp"; Description: "{cm:CreateMsftpDesktopShortcut}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: checkedonce

[Files]
Source: "..\Mshell\dist\{#MshellExeName}"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\Msftp\dist\{#MsftpExeName}"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{group}\{cm:MshellShortcutName}"; Filename: "{app}\{#MshellExeName}"; Parameters: "-open"; WorkingDir: "{app}"
Name: "{group}\{cm:MsftpShortcutName}"; Filename: "{app}\{#MsftpExeName}"; Parameters: "-open"; WorkingDir: "{app}"
Name: "{app}\{cm:MshellShortcutName}"; Filename: "{app}\{#MshellExeName}"; Parameters: "-open"; WorkingDir: "{app}"
Name: "{app}\{cm:MsftpShortcutName}"; Filename: "{app}\{#MsftpExeName}"; Parameters: "-open"; WorkingDir: "{app}"
Name: "{userdesktop}\{cm:MshellShortcutName}"; Filename: "{app}\{#MshellExeName}"; Parameters: "-open"; WorkingDir: "{app}"; Tasks: desktopicon_mshell
Name: "{userdesktop}\{cm:MsftpShortcutName}"; Filename: "{app}\{#MsftpExeName}"; Parameters: "-open"; WorkingDir: "{app}"; Tasks: desktopicon_msftp

[Run]
Filename: "{app}\{#MshellExeName}"; Parameters: "-open"; Description: "{cm:LaunchProgram,Mshell}"; Flags: nowait postinstall skipifsilent

[InstallDelete]
Type: files; Name: "{app}\Mshell-icon.ico"
Type: files; Name: "{app}\Msftp-icon.ico"
Type: files; Name: "{app}\Mshell-sakura-v1.ico"
Type: files; Name: "{app}\Msftp-sakura-v1.ico"
Type: files; Name: "{app}\Mshell-sakura-v2.ico"
Type: files; Name: "{app}\Msftp-sakura-v2.ico"
Type: files; Name: "{app}\Mshell-tech-v1.ico"
Type: files; Name: "{app}\Msftp-tech-v1.ico"
Type: files; Name: "{app}\Mshell-tech-v2.ico"
Type: files; Name: "{app}\Msftp-tech-v2.ico"
Type: files; Name: "{userdesktop}\Mshell.lnk"
Type: files; Name: "{userdesktop}\Msftp.lnk"
Type: files; Name: "{group}\Mshell.lnk"
Type: files; Name: "{group}\Msftp.lnk"
Type: files; Name: "{userdesktop}\myshell.lnk"
Type: files; Name: "{userdesktop}\myftp.lnk"
Type: files; Name: "{group}\myshell.lnk"
Type: files; Name: "{group}\myftp.lnk"
Type: files; Name: "{userdesktop}\mshell.lnk"
Type: files; Name: "{userdesktop}\msftp.lnk"
Type: files; Name: "{userdesktop}\mshell SSH Terminal.lnk"
Type: files; Name: "{userdesktop}\msftp File Manager.lnk"
Type: files; Name: "{userdesktop}\mshell SSH 终端.lnk"
Type: files; Name: "{userdesktop}\msftp 文件管理器.lnk"
Type: files; Name: "{group}\mshell.lnk"
Type: files; Name: "{group}\msftp.lnk"
Type: files; Name: "{group}\mshell SSH Terminal.lnk"
Type: files; Name: "{group}\msftp File Manager.lnk"
Type: files; Name: "{group}\mshell SSH 终端.lnk"
Type: files; Name: "{group}\msftp 文件管理器.lnk"
Type: files; Name: "{app}\mshell SSH Terminal.lnk"
Type: files; Name: "{app}\msftp File Manager.lnk"
Type: files; Name: "{app}\mshell SSH 终端.lnk"
Type: files; Name: "{app}\msftp 文件管理器.lnk"
Type: files; Name: "{app}\Mshell-tech-v3.ico"
Type: files; Name: "{app}\Msftp-tech-v3.ico"

[UninstallDelete]
Type: filesandordirs; Name: "{app}"

[Code]
const
  SHCNE_ASSOCCHANGED = $08000000;
  SHCNF_IDLIST = $0000;

procedure SHChangeNotify(wEventId: LongWord; uFlags: LongWord; dwItem1: Integer; dwItem2: Integer);
  external 'SHChangeNotify@shell32.dll stdcall';

procedure StopRunningApps();
var
  ResultCode: Integer;
begin
  Exec(ExpandConstant('{cmd}'), '/C taskkill /IM {#MshellExeName} /F /T >NUL 2>NUL', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  Exec(ExpandConstant('{cmd}'), '/C taskkill /IM {#MsftpExeName} /F /T >NUL 2>NUL', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
end;

function InitializeSetup(): Boolean;
begin
  StopRunningApps();
  Result := True;
end;

function InitializeUninstall(): Boolean;
begin
  StopRunningApps();
  Result := True;
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  ResultCode: Integer;
begin
  if CurStep = ssPostInstall then
  begin
    SHChangeNotify(SHCNE_ASSOCCHANGED, SHCNF_IDLIST, 0, 0);
    Exec(ExpandConstant('{sys}\ie4uinit.exe'), '-show', '', SW_HIDE,
      ewWaitUntilTerminated, ResultCode);
  end;
end;
