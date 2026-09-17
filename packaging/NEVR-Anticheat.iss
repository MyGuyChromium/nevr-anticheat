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

[Dirs]
; SQLite needs this parent before the first launch. Evidence survives uninstall.
Name: "{localappdata}\NEVR-Anticheat"; Flags: uninsneveruninstall

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
const
  // Newest verified-looking program snapshots kept after an update. Must match
  // the default of Remove-NEVRStaleProgramSnapshots in Program-Snapshot.ps1.
  SnapshotsToKeep = 3;
  SnapshotManifestHeader = 'nevr-program-snapshot/v1';

type
  TNEVRSystemTime = record
    Year, Month, DayOfWeek, Day, Hour, Minute, Second, Milliseconds: Word;
  end;

procedure NEVRGetSystemTime(var SystemTime: TNEVRSystemTime);
  external 'GetSystemTime@kernel32.dll stdcall';

// Snapshot folders are named in UTC, with a trailing Z, like the PowerShell
// writer in Program-Snapshot.ps1. GetDateTimeString is LOCAL time, which made
// "newest by name" wrong by the zone offset whenever both writers were used.
function UtcSnapshotStamp: String;
var
  Stamp: TNEVRSystemTime;
begin
  NEVRGetSystemTime(Stamp);
  Result := Format('%.4d%.2d%.2d-%.2d%.2d%.2dZ', [Stamp.Year, Stamp.Month, Stamp.Day, Stamp.Hour, Stamp.Minute, Stamp.Second]);
end;

function IsLinkedPath(const Path: String): Boolean;
var
  FindRec: TFindRec;
begin
  Result := False;
  if FindFirst(Path, FindRec) then
  begin
    try
      Result := (FindRec.Attributes and 1024) <> 0;
    finally
      FindClose(FindRec);
    end;
  end;
end;

// A folder this installer (or Program-Snapshot.ps1) wrote: it has a manifest
// with the expected header. Nothing else is ever a pruning candidate.
function LooksLikeProgramSnapshot(const Snapshot: String): Boolean;
var
  Manifest: AnsiString;
begin
  Result := (not IsLinkedPath(Snapshot)) and
    LoadStringFromFile(AddBackslash(Snapshot) + 'SHA256SUMS.txt', Manifest) and
    (Copy(Manifest, 1, Length(SnapshotManifestHeader)) = SnapshotManifestHeader);
end;

function IsProgramSnapshotFileName(const Name: String): Boolean;
begin
  Result := (CompareText(Name, 'nevr-desktop.exe') = 0) or (CompareText(Name, 'nevr-ac.exe') = 0) or
    (CompareText(Name, 'nevr-server.exe') = 0) or (CompareText(Name, 'nevr-bridge.exe') = 0) or
    (CompareText(Name, 'nevr-compat.exe') = 0) or (CompareText(Name, 'README.md') = 0) or
    (CompareText(Name, 'README-WINDOWS.txt') = 0) or (CompareText(Name, 'nevr.ico') = 0) or
    (CompareText(Name, 'SHA256SUMS.txt') = 0);
end;

// Visits Folder. With Remove = False it only answers whether every entry is a
// name a program snapshot can contain (Depth 0: program files and a "configs"
// folder; Depth 1: *.toml files). With Remove = True it deletes those files.
function VisitProgramSnapshot(const Folder: String; const Depth: Integer; const Remove: Boolean): Boolean;
var
  FindRec: TFindRec;
  Expected: Boolean;
begin
  Result := not IsLinkedPath(Folder);
  if not Result then
    Exit;
  if FindFirst(AddBackslash(Folder) + '*', FindRec) then
  begin
    try
      repeat
        if (FindRec.Name <> '.') and (FindRec.Name <> '..') then
        begin
          if (FindRec.Attributes and 1024) <> 0 then
            Expected := False
          else if (FindRec.Attributes and FILE_ATTRIBUTE_DIRECTORY) <> 0 then
          begin
            // Nested, not "and": the recursive visit must never run for a folder
            // that is not the expected one, whatever the evaluation order.
            Expected := (Depth = 0) and (CompareText(FindRec.Name, 'configs') = 0);
            if Expected then
              Expected := VisitProgramSnapshot(AddBackslash(Folder) + FindRec.Name, 1, Remove);
          end
          else if Depth = 0 then
            Expected := IsProgramSnapshotFileName(FindRec.Name)
          else
            Expected := CompareText(ExtractFileExt(FindRec.Name), '.toml') = 0;
          if not Expected then
            Result := False
          else if Remove and ((FindRec.Attributes and FILE_ATTRIBUTE_DIRECTORY) = 0) then
            DeleteFile(AddBackslash(Folder) + FindRec.Name);
        end;
      until not FindNext(FindRec);
    finally
      FindClose(FindRec);
    end;
  end;
end;

// Removes one snapshot WITHOUT a recursive delete. A folder holding anything
// a snapshot cannot contain is not ours to remove and is left exactly as it
// was: the check runs to completion before the first file is deleted.
procedure RemoveProgramSnapshot(const Snapshot: String);
begin
  if not VisitProgramSnapshot(Snapshot, 0, False) then
  begin
    Log('Program snapshot kept because it holds unexpected content: ' + Snapshot);
    Exit;
  end;
  VisitProgramSnapshot(Snapshot, 0, True);
  RemoveDir(AddBackslash(Snapshot) + 'configs');
  if not RemoveDir(Snapshot) then
    Log('Program snapshot folder could not be removed: ' + Snapshot);
end;

function IsNewerFileTime(const AHigh, ALow, BHigh, BLow: Cardinal): Boolean;
begin
  Result := (AHigh > BHigh) or ((AHigh = BHigh) and (ALow > BLow));
end;

// Keeps the newest Keep snapshots plus Current and removes the older ones.
// Age is the folder's last-write time, a UTC FILETIME set when the snapshot's
// manifest was written: independent of time zone and of either naming scheme.
procedure PruneProgramSnapshots(const RollbackRoot, Current: String; const Keep: Integer);
var
  FindRec: TFindRec;
  Names: array of String;
  TimeHigh, TimeLow: array of Cardinal;
  Kept: array of Boolean;
  Count, I, Pass, Best: Integer;
begin
  if IsLinkedPath(RemoveBackslash(RollbackRoot)) then
    Exit;
  Count := 0;
  if FindFirst(AddBackslash(RollbackRoot) + '*', FindRec) then
  begin
    try
      repeat
        if ((FindRec.Attributes and FILE_ATTRIBUTE_DIRECTORY) <> 0) and (FindRec.Name <> '.') and (FindRec.Name <> '..') and
          LooksLikeProgramSnapshot(AddBackslash(RollbackRoot) + FindRec.Name) then
        begin
          SetArrayLength(Names, Count + 1);
          SetArrayLength(TimeHigh, Count + 1);
          SetArrayLength(TimeLow, Count + 1);
          SetArrayLength(Kept, Count + 1);
          Names[Count] := FindRec.Name;
          TimeHigh[Count] := FindRec.LastWriteTime.dwHighDateTime;
          TimeLow[Count] := FindRec.LastWriteTime.dwLowDateTime;
          Kept[Count] := CompareText(AddBackslash(RollbackRoot) + FindRec.Name, Current) = 0;
          Count := Count + 1;
        end;
      until not FindNext(FindRec);
    finally
      FindClose(FindRec);
    end;
  end;
  for Pass := 1 to Keep do
  begin
    Best := -1;
    for I := 0 to Count - 1 do
      if (not Kept[I]) and ((Best < 0) or IsNewerFileTime(TimeHigh[I], TimeLow[I], TimeHigh[Best], TimeLow[Best])) then
        Best := I;
    if Best >= 0 then
      Kept[Best] := True;
  end;
  for I := 0 to Count - 1 do
    if not Kept[I] then
    begin
      Log('Removing old program snapshot: ' + Names[I]);
      RemoveProgramSnapshot(AddBackslash(RollbackRoot) + Names[I]);
    end;
end;

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
  Prefix := Prefix + UtcSnapshotStamp + '-';
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
  // Housekeeping only after the new snapshot is complete, and never fatal:
  // ~40 MB per update otherwise accumulates inside the evidence directory.
  try
    PruneProgramSnapshots(ExpandConstant('{localappdata}\NEVR-Anticheat\program-rollbacks'), SnapshotRoot, SnapshotsToKeep - 1);
  except
    Log('Old program snapshots were not pruned: ' + GetExceptionMessage);
  end;
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
