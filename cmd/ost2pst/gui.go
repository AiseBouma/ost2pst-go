//go:build !nogui

package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/AiseBouma/ost2pst-go/pst"
)

// guiReporter collects log lines from the conversion goroutine; the UI
// refreshes from it on a timer so a big export does not flood the UI.
type guiReporter struct {
	mu       sync.Mutex
	lines    []string
	live     bool
	dirty    bool
	cur, tot int
	phase    string
}

func (r *guiReporter) Status(msg string) {
	r.mu.Lock()
	r.lines = append(r.lines, msg)
	r.live, r.dirty = false, true
	r.mu.Unlock()
}

func (r *guiReporter) StatusReplace(msg string) {
	r.mu.Lock()
	if r.live && len(r.lines) > 0 {
		r.lines[len(r.lines)-1] = msg
	} else {
		r.lines = append(r.lines, msg)
	}
	r.live, r.dirty = true, true
	r.mu.Unlock()
}

func (r *guiReporter) Progress(cur, tot int, phase string) {
	r.mu.Lock()
	r.cur, r.tot, r.phase, r.dirty = cur, tot, phase, true
	r.mu.Unlock()
}

func (r *guiReporter) snapshot() (lines []string, cur, tot int, phase string, dirty bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.dirty
	r.dirty = false
	return r.lines, r.cur, r.tot, r.phase, d
}

type ui struct {
	win       fyne.Window
	rep       *guiReporter
	conv      *pst.Converter
	srcPath   string
	selected  uint32
	busy      bool
	openBtn   *widget.Button
	exportBtn *widget.Button
	resetPW   *widget.Check
	tree      *widget.Tree
	srcInfo   *widget.Label
	outInfo   *widget.Label
	logList   *widget.List
	progress  *widget.ProgressBar
	progLabel *widget.Label
}

func runGUI(initial string) {
	a := app.NewWithID("io.github.niv2023.ost2pst")
	u := &ui{rep: &guiReporter{}}
	u.conv = pst.NewConverter(u.rep)
	u.win = a.NewWindow("OST to PST v" + version)
	u.build()
	u.win.Resize(fyne.NewSize(1050, 760))
	go u.refreshLoop()
	if initial != "" {
		u.load(initial)
	}
	u.win.ShowAndRun()
	u.conv.Close()
}

func mono(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.TextStyle = fyne.TextStyle{Monospace: true}
	l.Wrapping = fyne.TextWrapOff
	l.Truncation = fyne.TextTruncateEllipsis
	return l
}

func (u *ui) build() {
	u.openBtn = widget.NewButtonWithIcon("Select OST (or PST) file", theme.FolderOpenIcon(), u.openFile)
	u.openBtn.Importance = widget.HighImportance
	u.srcInfo = mono("No file opened")
	u.outInfo = mono("")
	u.resetPW = widget.NewCheck("Reset password (PST input only)", nil)
	u.resetPW.Disable()
	u.exportBtn = widget.NewButtonWithIcon("Export to PST", theme.DocumentSaveIcon(), u.export)
	u.exportBtn.Disable()

	u.tree = widget.NewTree(u.treeChildren, u.treeIsBranch,
		func(bool) fyne.CanvasObject { return widget.NewLabel("folder name placeholder") },
		u.treeUpdate)
	u.tree.OnSelected = u.treeSelected

	u.logList = widget.NewList(
		func() int { l, _, _, _, _ := u.rep.peek(); return len(l) },
		func() fyne.CanvasObject {
			l := widget.NewLabel("")
			l.TextStyle = fyne.TextStyle{Monospace: true}
			return l
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			l, _, _, _, _ := u.rep.peek()
			if i < len(l) {
				o.(*widget.Label).SetText(l[i])
			}
		})
	u.progress = widget.NewProgressBar()
	u.progLabel = widget.NewLabel("")
	u.progress.Hide()
	u.progLabel.Hide()

	left := container.NewBorder(
		container.NewVBox(u.openBtn, u.srcInfo, widget.NewLabelWithStyle("Input folder tree", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})),
		container.NewVBox(u.resetPW, u.exportBtn, u.outInfo),
		nil, nil, u.tree)
	right := container.NewBorder(
		widget.NewLabelWithStyle("Log", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		container.NewVBox(u.progLabel, u.progress), nil, nil, u.logList)
	split := container.NewHSplit(left, right)
	split.Offset = 0.42
	u.win.SetContent(split)
}

func (r *guiReporter) peek() ([]string, int, int, string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lines, r.cur, r.tot, r.phase, r.dirty
}

func (u *ui) refreshLoop() {
	for range time.Tick(200 * time.Millisecond) {
		lines, cur, tot, phase, dirty := u.rep.snapshot()
		if !dirty {
			continue
		}
		u.logList.Refresh()
		if len(lines) > 0 {
			u.logList.ScrollToBottom()
		}
		if tot > 0 {
			u.progress.SetValue(float64(cur) / float64(tot))
			u.progLabel.SetText(fmt.Sprintf("%s: %d of %d", phase, cur, tot))
		}
	}
}

// --- folder tree --------------------------------------------------------------

func (u *ui) folder(uid widget.TreeNodeID) *pst.Folder {
	n, err := strconv.ParseUint(uid, 10, 32)
	if err != nil || u.conv.Source() == nil {
		return nil
	}
	return u.conv.FolderByNID(uint32(n))
}

func (u *ui) treeChildren(uid widget.TreeNodeID) []widget.TreeNodeID {
	if u.conv.Source() == nil || u.conv.Root == nil {
		return nil
	}
	if uid == "" {
		return []string{strconv.FormatUint(uint64(u.conv.Root.Nid), 10)}
	}
	f := u.folder(uid)
	if f == nil {
		return nil
	}
	ids := make([]string, len(f.Children))
	for i, ch := range f.Children {
		ids[i] = strconv.FormatUint(uint64(ch.Nid), 10)
	}
	return ids
}

func (u *ui) treeIsBranch(uid widget.TreeNodeID) bool {
	if uid == "" {
		return true
	}
	f := u.folder(uid)
	return f != nil && len(f.Children) > 0
}

func (u *ui) treeUpdate(uid widget.TreeNodeID, _ bool, o fyne.CanvasObject) {
	f := u.folder(uid)
	l := o.(*widget.Label)
	switch {
	case f == nil:
		l.SetText("")
	case f.IsRoot():
		l.SetText("(root of the store)")
	default:
		l.SetText(f.Name)
	}
}

func (u *ui) treeSelected(uid widget.TreeNodeID) {
	if u.busy || u.conv.Source() == nil || u.conv.Source().Type != pst.FileOST {
		return
	}
	f := u.folder(uid)
	switch {
	case f == nil:
		u.selected = 0
		u.exportBtn.SetText("Select a folder to export")
		u.exportBtn.Disable()
	case f.IsRoot(): // the hidden root only holds system folders
		u.selected = 0
		u.exportBtn.SetText("Select a mailbox folder to export")
		u.exportBtn.Disable()
	default:
		u.selected = f.Nid
		name := f.Name
		if f.Parent != nil && !f.Parent.IsRoot() {
			name = f.Parent.Name + " / " + f.Name
		}
		u.exportBtn.SetText(fmt.Sprintf("Export %q to PST", name))
		u.exportBtn.Enable()
	}
}

// selectFolder expands the tree down to f and selects it.
func (u *ui) selectFolder(f *pst.Folder) {
	var path []*pst.Folder
	for p := f; p != nil; p = p.Parent {
		path = append(path, p)
		if p.IsRoot() {
			break
		}
	}
	for i := len(path) - 1; i > 0; i-- {
		u.tree.OpenBranch(strconv.FormatUint(uint64(path[i].Nid), 10))
	}
	id := strconv.FormatUint(uint64(f.Nid), 10)
	u.tree.OpenBranch(id)
	u.tree.Select(id)
	u.tree.ScrollTo(id)
	u.treeSelected(id)
}

// --- actions ------------------------------------------------------------------

func (u *ui) openFile() {
	d := dialog.NewFileOpen(func(rc fyne.URIReadCloser, err error) {
		if err != nil || rc == nil {
			return
		}
		path := rc.URI().Path()
		rc.Close()
		u.load(path)
	}, u.win)
	d.SetFilter(storage.NewExtensionFileFilter([]string{".ost", ".pst", ".OST", ".PST"}))
	d.Resize(fyne.NewSize(900, 600))
	d.Show()
}

func (u *ui) load(path string) {
	u.setBusy(true)
	u.rep.Status("reading " + path)
	go func() {
		err := u.conv.Open(path)
		u.setBusy(false)
		if err != nil {
			u.rep.Status(err.Error())
			dialog.ShowError(err, u.win)
			return
		}
		u.srcPath = path
		u.rep.Status("folder list completed")
		u.afterLoad()
	}()
}

// afterLoad refreshes the UI after a file was (re)opened.
func (u *ui) afterLoad() {
	src := u.conv.Source()
	u.srcInfo.SetText(shortSummary(src.Summary()))
	u.tree.UnselectAll()
	u.tree.Refresh()
	u.tree.OpenBranch(strconv.FormatUint(uint64(u.conv.Root.Nid), 10))
	if src.Type == pst.FileOST {
		u.resetPW.SetChecked(false)
		u.resetPW.Disable()
		if f := u.conv.FolderByNID(u.selected); u.selected != 0 && f != nil {
			u.selectFolder(f) // keep the previous selection after an export
		} else if top := u.conv.MailboxTop(); top != nil {
			u.selectFolder(top)
		} else {
			u.selected = 0
			u.exportBtn.SetText("Select a mailbox folder to export")
			u.exportBtn.Disable()
		}
	} else {
		u.selected = 0
		u.resetPW.Enable()
		u.exportBtn.SetText("Export ALL to PST (compact)")
		u.exportBtn.Enable()
	}
}

func (u *ui) export() {
	src := u.conv.Source()
	if src == nil {
		return
	}
	if src.Type == pst.FileOST && u.selected == 0 {
		dialog.ShowInformation("No folder selected", "Please select a mailbox folder in the tree first.", u.win)
		return
	}
	d := dialog.NewFileSave(func(wc fyne.URIWriteCloser, err error) {
		if err != nil || wc == nil {
			return
		}
		dest := wc.URI().Path()
		wc.Close()
		if !strings.EqualFold(filepath.Ext(dest), ".pst") {
			dest += ".pst"
		}
		u.runExport(dest)
	}, u.win)
	base := strings.TrimSuffix(filepath.Base(u.srcPath), filepath.Ext(u.srcPath))
	if src.Type == pst.FilePST {
		base += "-compacted"
	}
	d.SetFileName(base + ".pst")
	if l, err := storage.ListerForURI(storage.NewFileURI(filepath.Dir(u.srcPath))); err == nil {
		d.SetLocation(l)
	}
	d.Resize(fyne.NewSize(900, 600))
	d.Show()
}

func (u *ui) runExport(dest string) {
	u.setBusy(true)
	u.progress.Show()
	u.progLabel.Show()
	reset := u.resetPW.Checked
	go func() {
		start := time.Now()
		err := u.conv.Export(dest, u.selected, reset)
		u.setBusy(false)
		if err != nil {
			u.rep.Status("ERROR: " + err.Error())
			dialog.ShowError(err, u.win)
		} else {
			u.rep.Status(fmt.Sprintf("done in %s", time.Since(start).Round(time.Second)))
			u.outInfo.SetText(shortSummary(u.conv.LastOutput))
			dialog.ShowInformation("Export completed", "The PST was written to:\n"+dest+
				"\n\nYou can check it with Microsoft's SCANPST tool.", u.win)
		}
		if u.conv.Source() != nil {
			u.afterLoad()
		}
	}()
}

func (u *ui) setBusy(b bool) {
	u.busy = b
	if b {
		u.openBtn.Disable()
		u.exportBtn.Disable()
	} else {
		u.openBtn.Enable()
		if u.conv.Source() != nil && (u.selected != 0 || u.conv.Source().Type == pst.FilePST) {
			u.exportBtn.Enable()
		}
	}
}

// shortSummary shows only the file name on the first line; long paths would
// otherwise widen or overflow the panel.
func shortSummary(s string) string {
	lines := strings.SplitN(s, "\n", 2)
	if k := strings.Index(lines[0], ": "); k >= 0 {
		lines[0] = lines[0][:k+2] + filepath.Base(strings.TrimSpace(lines[0][k+2:]))
	}
	return strings.Join(lines, "\n")
}
