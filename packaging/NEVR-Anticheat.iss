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
CloseApplicationsFilter=nevr-*.exe
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
Source: "{#SourceDir}\installer\Program-Snapshot.ps1"; DestDir: "{app}"; Flags: ignoreversion
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
procedure RejectSnapshotLink(const Path: String);
var
  FindRec: TFindRec;
begin
  if FindFirst(Path, FindRec) then
  begin
    try
      if (FindRec.Attributes and 1024) <> 0 then
        RaiseException('Refusing linked program/snapshot path: ' + Path);
    finally
      FindClose(FindRec);
    end;
  end;
end;

procedure SnapshotProgramFile(const RelativeName, SnapshotRoot: String; var Manifest: String);
var
  SourcePath: String;
  DestinationPath: String;
  Digest: String;
begin
  SourcePath := ExpandConstant('{app}\') + RelativeName;
  DestinationPath := AddBackslash(SnapshotRoot) + RelativeName;
  if not FileExists(SourcePath) then
    RaiseException('Cannot preserve missing program file: ' + SourcePath);
  RejectSnapshotLink(ExtractFileDir(SourcePath));
  RejectSnapshotLink(SourcePath);
  if not ForceDirectories(ExtractFileDir(DestinationPath)) then
    RaiseException('Cannot create program snapshot directory. Installation stopped.');
  Digest := GetSHA256OfFile(SourcePath);
  if not FileCopy(SourcePath, DestinationPath, True) then
    RaiseException('Cannot preserve program file: ' + SourcePath);
  if CompareText(Digest, GetSHA256OfFile(DestinationPath)) <> 0 then
    RaiseException('Program snapshot checksum failed: ' + SourcePath);
  SourcePath := RelativeName;
  StringChangeEx(SourcePath, '\', '/', True);
  Manifest := Manifest + Lowercase(Digest) + ' *' + SourcePath + #13#10;
end;

procedure SnapshotPreviousProgram;
var
  SnapshotRoot: String;
  Prefix: String;
  Manifest: String;
  Attempt: Integer;
begin
  if not FileExists(ExpandConstant('{app}\nevr-desktop.exe')) then
    Exit;

  RejectSnapshotLink(ExpandConstant('{app}'));
  RejectSnapshotLink(ExpandConstant('{localappdata}\NEVR-Anticheat'));
  RejectSnapshotLink(ExpandConstant('{localappdata}\NEVR-Anticheat\program-rollbacks'));
  Prefix := ExpandConstant('{localappdata}\NEVR-Anticheat\program-rollbacks\');
  if not ForceDirectories(Prefix) then
    RaiseException('Cannot create program rollback directory. Installation stopped.');
  Prefix := Prefix + GetDateTimeString('yyyymmdd-hhnnss', '-', ':') + '-';
  Attempt := 0;
  repeat
    SnapshotRoot := Prefix + IntToStr(Attempt);
    Attempt := Attempt + 1;
  until not DirExists(SnapshotRoot);
  if not CreateDir(SnapshotRoot) then
    RaiseException('Cannot reserve a unique program snapshot. Installation stopped.');
  Manifest := 'nevr-program-snapshot/v1' + #13#10;
  SnapshotProgramFile('nevr-desktop.exe', SnapshotRoot, Manifest);
  SnapshotProgramFile('nevr-ac.exe', SnapshotRoot, Manifest);
  SnapshotProgramFile('nevr-server.exe', SnapshotRoot, Manifest);
  SnapshotProgramFile('nevr-bridge.exe', SnapshotRoot, Manifest);
  SnapshotProgramFile('nevr-compat.exe', SnapshotRoot, Manifest);
  if FileExists(ExpandConstant('{app}\README.md')) then
    SnapshotProgramFile('README.md', SnapshotRoot, Manifest);
  if FileExists(ExpandConstant('{app}\README-WINDOWS.txt')) then
    SnapshotProgramFile('README-WINDOWS.txt', SnapshotRoot, Manifest);
  if FileExists(ExpandConstant('{app}\nevr.ico')) then
    SnapshotProgramFile('nevr.ico', SnapshotRoot, Manifest);
  if FileExists(ExpandConstant('{app}\configs\default.toml')) then
    SnapshotProgramFile('configs\default.toml', SnapshotRoot, Manifest);
  if FileExists(ExpandConstant('{app}\configs\shadow_deploy.toml')) then
    SnapshotProgramFile('configs\shadow_deploy.toml', SnapshotRoot, Manifest);
  if not SaveStringToFile(AddBackslash(SnapshotRoot) + 'SHA256SUMS.txt', Manifest, False) then
    RaiseException('Cannot finish program snapshot manifest. Installation stopped.');
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
begin
  Result := '';
  // Inno's Restart Manager handles only applications using installation
  // payload files. Never terminate unrelated NEVR copies by process name.
  try
    SnapshotPreviousProgram;
  except
    Result := GetExceptionMessage;
  end;
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
