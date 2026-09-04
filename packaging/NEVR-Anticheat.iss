#ifndef SourceDir
  #error SourceDir must point to the prepared Windows package directory.
#endif

#ifndef MyAppVersion
  #define MyAppVersion "0.0.0"
#endif

#define MyAppName "NEVR-Anticheat"
#define MyAppPublisher "MyGuyChromium"
#define MyAppURL "https://github.com/MyGuyChromium/nevr-anticheat"

[Setup]
AppId={{5D1DE622-790D-422D-B3D9-6DFA265169F8}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppVerName={#MyAppName} {#MyAppVersion}
AppPublisher={#MyAppPublisher}
AppPublisherURL={#MyAppURL}
AppSupportURL={#MyAppURL}/issues
AppUpdatesURL={#MyAppURL}/releases/tag/windows-latest
DefaultDirName={localappdata}\Programs\NEVR-Anticheat
DefaultGroupName=NEVR-Anticheat
PrivilegesRequired=lowest
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0.17763
DisableDirPage=yes
DisableProgramGroupPage=yes
DisableReadyPage=yes
DisableWelcomePage=no
AllowNoIcons=no
UsePreviousAppDir=yes
UsePreviousGroup=yes
CloseApplications=yes
CloseApplicationsFilter=nevr-desktop.exe
RestartApplications=no
SetupLogging=yes
WizardStyle=modern
WizardImageFile={#SourcePath}\assets\nevr-wizard.bmp
WizardSmallImageFile={#SourcePath}\assets\nevr-wizard-small.bmp
WizardImageStretch=yes
WizardKeepAspectRatio=yes
SetupIconFile={#SourcePath}\assets\nevr.ico
UninstallDisplayIcon={app}\nevr.ico
UninstallDisplayName=NEVR-Anticheat
OutputBaseFilename=NEVR-Anticheat-Setup
Compression=lzma2/normal
SolidCompression=no
ASLRCompatible=yes
DEPCompatible=yes
#ifdef EnableSigning
SignedUninstaller=yes
SignTool=nevr
#else
SignedUninstaller=no
#endif
VersionInfoCompany={#MyAppPublisher}
VersionInfoDescription=NEVR-Anticheat Windows installer
VersionInfoOriginalFileName=NEVR-Anticheat-Setup.exe
VersionInfoProductName={#MyAppName}
VersionInfoProductTextVersion={#MyAppVersion}
VersionInfoProductVersion={#MyAppVersion}
VersionInfoTextVersion={#MyAppVersion}
VersionInfoVersion={#MyAppVersion}

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Messages]
SetupWindowTitle=Install NEVR-Anticheat
WelcomeLabel1=NEVR-Anticheat
WelcomeLabel2=Private, local replay intelligence.%n%nInstall takes only a few seconds. Existing evidence and settings stay safe during upgrades.
ButtonNext=&Install
FinishedHeadingLabel=NEVR-Anticheat is ready
FinishedLabel=Installation is complete. NEVR can launch now.

[Files]
Source: "{#SourceDir}\nevr-desktop.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\nevr-ac.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\nevr-server.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\nevr-bridge.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\nevr-compat.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\configs\*.toml"; DestDir: "{app}\configs"; Flags: ignoreversion
Source: "{#SourceDir}\README.md"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\README-WINDOWS.txt"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\installer\Rollback-NEVR.cmd"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\installer\Rollback-NEVR.ps1"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourcePath}\assets\nevr.ico"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{group}\NEVR-Anticheat"; Filename: "{app}\nevr-desktop.exe"; Parameters: "--config ""{app}\installed.toml"""; WorkingDir: "{app}"; IconFilename: "{app}\nevr.ico"
Name: "{autodesktop}\NEVR-Anticheat"; Filename: "{app}\nevr-desktop.exe"; Parameters: "--config ""{app}\installed.toml"""; WorkingDir: "{app}"; IconFilename: "{app}\nevr.ico"

[Run]
Filename: "{app}\nevr-desktop.exe"; Parameters: "--config ""{app}\installed.toml"""; Description: "Launch NEVR-Anticheat"; WorkingDir: "{app}"; Flags: nowait postinstall skipifsilent

[UninstallDelete]
Type: files; Name: "{app}\installed.toml"
Type: files; Name: "{app}\nevr.ico"
Type: dirifempty; Name: "{app}\configs"
Type: dirifempty; Name: "{app}"

[Code]
procedure CopyDirectoryTree(const SourceDirName, DestinationDirName: String);
var
  FindRec: TFindRec;
  SourcePath: String;
  DestinationPath: String;
begin
  if not DirExists(SourceDirName) then
    Exit;

  ForceDirectories(DestinationDirName);
  if FindFirst(AddBackslash(SourceDirName) + '*', FindRec) then
  begin
    try
      repeat
        if (FindRec.Name <> '.') and (FindRec.Name <> '..') then
        begin
          SourcePath := AddBackslash(SourceDirName) + FindRec.Name;
          DestinationPath := AddBackslash(DestinationDirName) + FindRec.Name;
          if (FindRec.Attributes and FILE_ATTRIBUTE_DIRECTORY) <> 0 then
            CopyDirectoryTree(SourcePath, DestinationPath)
          else
            FileCopy(SourcePath, DestinationPath, False);
        end;
      until not FindNext(FindRec);
    finally
      FindClose(FindRec);
    end;
  end;
end;

procedure SnapshotPreviousProgram;
var
  SnapshotRoot: String;
begin
  if not FileExists(ExpandConstant('{app}\nevr-desktop.exe')) then
    Exit;

  SnapshotRoot := ExpandConstant('{localappdata}\NEVR-Anticheat\program-rollbacks\') +
    GetDateTimeString('yyyymmdd-hhnnss', '-', ':');
  CopyDirectoryTree(ExpandConstant('{app}'), SnapshotRoot);
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  ResultCode: Integer;
begin
  Result := '';
  Exec(ExpandConstant('{sys}\taskkill.exe'), '/F /IM nevr-desktop.exe', '',
    SW_HIDE, ewWaitUntilTerminated, ResultCode);
  SnapshotPreviousProgram;
end;

procedure EnsureInstalledConfig;
var
  ConfigPath: String;
  DatabasePath: String;
  ConfigText: String;
begin
  ConfigPath := ExpandConstant('{app}\installed.toml');
  if FileExists(ConfigPath) then
    Exit;

  DatabasePath := ExpandConstant('{localappdata}\NEVR-Anticheat\nevr-anticheat.db');
  StringChangeEx(DatabasePath, '\', '/', True);
  ConfigText := '# Generated by the NEVR-Anticheat Windows installer.' + #13#10 +
    '# Evidence is stored separately so upgrades and uninstall are safe.' + #13#10 +
    '[general]' + #13#10 + 'db_path = "' + DatabasePath + '"' + #13#10;
  if not SaveStringToFile(ConfigPath, ConfigText, False) then
    RaiseException('Could not create the NEVR-Anticheat configuration.');
end;

procedure CurStepChanged(CurStep: TSetupStep);
begin
  if CurStep = ssPostInstall then
    EnsureInstalledConfig;
end;
