# ost2pst (Go)

A Go port of [Niv2023/ost2pst](https://github.com/Niv2023/ost2pst). It converts Outlook OST files to PST and compacts PST files, with an optional password reset. It reads and writes the binary [MS-PST] format directly, so it needs neither Outlook nor MAPI. It has a graphical interface built with [Fyne](https://fyne.io) and runs on Windows, macOS and Linux. It also has a command-line mode.

Only Unicode OST/PST files are supported (Outlook 2003 and later).

## Using it

**GUI:** run `ost2pst` (on Windows, `ost2pst.exe`).

1. Click **Select OST (or PST) file**. You can also drop the file onto the program, or pass it as an argument.
2. For an OST, the mailbox folder is selected automatically. You can pick a different folder in the tree, and that folder and all its subfolders will be exported.
3. Click **Export … to PST** and choose where to save the result.

For a PST input, the whole file is compacted. Tick **Reset password** to remove the store password.

**Command line** (`ost2pst-cli.exe` on Windows, or `ost2pst -i …` anywhere):

```
ost2pst -i mailbox.ost -list                                 # show the folders and their NIDs
ost2pst -i mailbox.ost -o mailbox.pst                        # export the mailbox folder
ost2pst -i mailbox.ost -folder "Root - Mailbox/IPM_SUBTREE/Inbox" -o inbox.pst
ost2pst -i mailbox.ost -folder 8514 -o out.pst               # select a folder by NID
ost2pst -i archive.pst -o compacted.pst -reset-password
```

Copy the OST somewhere else before converting it. Outlook keeps it locked while it is running. OST files usually live in `C:\Users\<you>\AppData\Local\Microsoft\Outlook`. You can check the result with Microsoft's SCANPST, which should report at most minor inconsistencies.

## Building

You need Go 1.22 or later and a C compiler, because Fyne uses OpenGL through cgo.

- **Windows:** install [MSYS2](https://www.msys2.org/) or TDM-GCC so that `gcc` is on your PATH.
- **macOS:** `xcode-select --install`
- **Linux (Debian/Ubuntu):** `sudo apt install gcc libgl1-mesa-dev xorg-dev`

```
go mod tidy
go build -o ost2pst ./cmd/ost2pst                              # GUI + CLI
go build -tags nogui -o ost2pst-cli ./cmd/ost2pst              # CLI only, no cgo needed
```

On Windows, add `-ldflags "-H=windowsgui"` to the GUI build so that no console window opens.

## Layout

| Path | Contents (C# original) |
|---|---|
| `pst/consts.go`, `types.go` | Format constants and on-disk structures (`Constants.cs`, `DataTypes.cs`) |
| `pst/crc.go` | CRC, permutative encoding, signatures (`DataAlgorithms.cs`) |
| `pst/xstfile.go` | OST/PST reader (`XstFile.cs`) |
| `pst/pstfile.go`, `ntree.go`, `blocks.go` | PST writer: header, AMaps, B-tree pages, block trees (`PstFile.cs`, `NTree.cs`, `Blocks.cs`) |
| `pst/ltpread.go`, `bth.go`, `tablecontext.go`, `property.go` | Heap-on-node, BTH, PC and TC (`LTP.cs`, `BTH.cs`, `Property.cs`) |
| `pst/templates.go` | Minimum PST objects (`Requirements.cs`) |
| `pst/converter.go` | Folder list, the two conversion passes, export (`FileManager.cs`) |
| `cmd/ost2pst` | GUI (`gui.go`) and CLI (`cli.go`) |

## Differences from the C# version

The conversion logic is a faithful port. These bugs were fixed along the way:

- **Folders are no longer lost.** OST folders with NIDs 0x8022, 0x8042 and 0x8062 collide with folders every PST reserves for itself. The C# version dropped 0x8062 (moving its subfolders to the root) and silently lost the other two. These folders are now renumbered and kept with their content.
- **Out-of-order folders are handled.** A folder listed before its parent no longer crashes the folder scan ("Parent folder NID … not found"). If a parent is really missing, the folder is attached to the root.
- **The hidden root can't be exported.** Exporting it used to produce an empty PST. The mailbox folder is now pre-selected automatically.
- **PST compaction works from the GUI again.** The folder-selection guard used to block it.
- **Shared blocks are reused correctly.** The first source block in the mapping list was never found again (`index > 0`), which could fail messages with "already mapped".
- **Large subnode trees no longer crash.** The SI block count was wrong, which crashed messages with exactly 680, 1020, … subnodes.
- **Password reset keeps the store.** Resetting the password on a PST without a password used to drop the message store from the output.
- **Several rare corruptions are fixed:**
  - an AMap could overflow when a page needed an alignment gap;
  - a heap block could exceed 8176 bytes by one byte;
  - a recipient-table subnode could keep a dangling source BID;
  - a table moved from the heap to a subnode was written with subnode NID 0.
- **Failures are reported instead of aborting.** An object that fails in either pass is logged and skipped instead of aborting the export, and the number of skipped items is reported at the end.
- **Large mailboxes are much faster.** Linear searches over all NBT/BBT entries were replaced with indexed lookups, removing O(n²) behaviour. For a 9GB OST file this reduced the time from hours to a couple of minutes.
- **Smaller safeguards:**
  - the destination may not be the source file;
  - the temporary file has a unique name and is deleted afterwards;
  - files with unsupported "high" (cyclic) encryption are rejected with a clear message.

## Testing

The port was tested on the sample files in the java-libpst test suite: an Outlook 2013 OST and two PSTs, one of them password protected. Every output was read back with libpff, an independent reader. Folder trees, messages, recipients and attachments all match the sources, apart from these intentional changes:

- required properties that were missing (message status, modification time) are added;
- empty string properties are dropped, as in the C# version;
- message sizes are recalculated.
