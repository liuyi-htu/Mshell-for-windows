#define MyAppName "myshell"
#define BuildAppVersion GetEnv("MY_APP_VERSION")
#if BuildAppVersion == ""
  #define MyAppVersion "1.0"
#else
  #define MyAppVersion BuildAppVersion
#endif
#define MyAppPublisher "Project"
#define MyShellExeName "myshell.exe"
#define MyFTPExeName "myftp.exe"
#define ProjectRoot AddBackslash(SourcePath) + ".."
#define MyShellExe ProjectRoot + "\myshell\dist\" + MyShellExeName
#define MyFTPExe ProjectRoot + "\myftp\dist\" + MyFTPExeName
#define MyShellIcon ProjectRoot + "\myshell\assets\app-icon.ico"
#define MyFTPIcon ProjectRoot + "\myftp\assets\app-icon.ico"

[Setup]
AppId={{C991B69D-07D3-4D47-94A9-FA2E6328D23A}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher={#MyAppPublisher}
DefaultDirName=D:\Program Files\{#MyAppName}
DefaultGroupName={#MyAppName}
AllowNoIcons=yes
OutputDir=Output
OutputBaseFilename=myshell-setup
Compression=lzma
SolidCompression=yes
WizardStyle=modern
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
PrivilegesRequired=admin
UninstallDisplayName={#MyAppName}
UninstallDisplayIcon={app}\myshell-icon.ico
SetupIconFile={#MyShellIcon}
CloseApplications=yes
CloseApplicationsFilter={#MyShellExeName},{#MyFTPExeName}
RestartIfNeededByRun=no

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[CustomMessages]
english.CreateMyShellDesktopShortcut=Create a myshell desktop shortcut
english.CreateMyFTPDesktopShortcut=Create a myftp desktop shortcut
english.MyShellShortcutName=myshell
english.MyFTPShortcutName=myftp

[Tasks]
Name: "desktopicon_myshell"; Description: "{cm:CreateMyShellDesktopShortcut}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: checkedonce
Name: "desktopicon_myftp"; Description: "{cm:CreateMyFTPDesktopShortcut}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: checkedonce

[Files]
Source: "{#MyShellExe}"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#MyFTPExe}"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#MyShellIcon}"; DestDir: "{app}"; DestName: "myshell-icon.ico"; Flags: ignoreversion
Source: "{#MyFTPIcon}"; DestDir: "{app}"; DestName: "myftp-icon.ico"; Flags: ignoreversion

[Icons]
Name: "{group}\{cm:MyShellShortcutName}"; Filename: "{app}\{#MyShellExeName}"; Parameters: "-open"; WorkingDir: "{app}"; IconFilename: "{app}\myshell-icon.ico"
Name: "{group}\{cm:MyFTPShortcutName}"; Filename: "{app}\{#MyFTPExeName}"; Parameters: "-open"; WorkingDir: "{app}"; IconFilename: "{app}\myftp-icon.ico"
Name: "{app}\{cm:MyShellShortcutName}"; Filename: "{app}\{#MyShellExeName}"; Parameters: "-open"; WorkingDir: "{app}"; IconFilename: "{app}\myshell-icon.ico"
Name: "{app}\{cm:MyFTPShortcutName}"; Filename: "{app}\{#MyFTPExeName}"; Parameters: "-open"; WorkingDir: "{app}"; IconFilename: "{app}\myftp-icon.ico"
Name: "{userdesktop}\{cm:MyShellShortcutName}"; Filename: "{app}\{#MyShellExeName}"; Parameters: "-open"; WorkingDir: "{app}"; IconFilename: "{app}\myshell-icon.ico"; Tasks: desktopicon_myshell
Name: "{userdesktop}\{cm:MyFTPShortcutName}"; Filename: "{app}\{#MyFTPExeName}"; Parameters: "-open"; WorkingDir: "{app}"; IconFilename: "{app}\myftp-icon.ico"; Tasks: desktopicon_myftp

[Run]
Filename: "{app}\{#MyShellExeName}"; Parameters: "-open"; Description: "{cm:LaunchProgram,myshell}"; Flags: nowait postinstall skipifsilent

[InstallDelete]
Type: files; Name: "{userdesktop}\myshell.lnk"
Type: files; Name: "{userdesktop}\myftp.lnk"
Type: files; Name: "{userdesktop}\myshell SSH Terminal.lnk"
Type: files; Name: "{userdesktop}\myftp File Manager.lnk"
Type: files; Name: "{userdesktop}\myshell SSH 终端.lnk"
Type: files; Name: "{userdesktop}\myftp 文件管理器.lnk"
Type: files; Name: "{group}\myshell.lnk"
Type: files; Name: "{group}\myftp.lnk"
Type: files; Name: "{group}\myshell SSH Terminal.lnk"
Type: files; Name: "{group}\myftp File Manager.lnk"
Type: files; Name: "{group}\myshell SSH 终端.lnk"
Type: files; Name: "{group}\myftp 文件管理器.lnk"
Type: files; Name: "{app}\myshell SSH Terminal.lnk"
Type: files; Name: "{app}\myftp File Manager.lnk"
Type: files; Name: "{app}\myshell SSH 终端.lnk"
Type: files; Name: "{app}\myftp 文件管理器.lnk"

[UninstallDelete]
Type: filesandordirs; Name: "{app}"

[Code]
procedure StopRunningApps();
var
  ResultCode: Integer;
begin
  Exec(ExpandConstant('{cmd}'), '/C taskkill /IM {#MyShellExeName} /F /T >NUL 2>NUL', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
  Exec(ExpandConstant('{cmd}'), '/C taskkill /IM {#MyFTPExeName} /F /T >NUL 2>NUL', '', SW_HIDE, ewWaitUntilTerminated, ResultCode);
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
