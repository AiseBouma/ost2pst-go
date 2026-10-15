package pst

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Reporter receives progress information. Implementations must be safe to
// call from the conversion goroutine.
type Reporter interface {
	Status(msg string)        // adds a line to the log
	StatusReplace(msg string) // replaces the last log line
	Progress(current, total int, phase string)
}

// Folder is a folder of the source file.
type Folder struct {
	NbtIndex     int
	Parent       *Folder
	Children     []*Folder
	Name         string
	Nid          uint32
	ToBeExported bool
	Properties   []Property
}

// IsRoot reports whether f is the hidden root folder of the store.
func (f *Folder) IsRoot() bool { return f.Parent == nil || f.Parent == f }

// Converter holds the state of one source file and its exports.
type Converter struct {
	rep         Reporter
	srcName     string
	src         *XstFile
	out         *PstFile
	w           *blockWriter
	Folders     []*Folder
	Root        *Folder
	folderByNid map[uint32]*Folder
	msgSizes    map[uint32]uint32
	defaultTime uint64
	tpl         templates
	replChange  uint64
	skipped     int
	remapped    map[uint32]uint32 // old folder NID -> new NID (reserved PST NIDs)
	// LastOutput describes the last PST written.
	LastOutput string
}

var replVersionHistory = []byte{1, 0, 0, 0, 243, 136, 137, 115, 10, 69, 209, 70, 151, 195, 23, 245, 7, 39, 50, 56, 1, 0, 0, 0}

// NewConverter creates a converter reporting to rep.
func NewConverter(rep Reporter) *Converter {
	return &Converter{rep: rep, replChange: 0x100}
}

func (c *Converter) status(msg string) { c.rep.Status(msg) }

// Source returns the open source file (nil if none).
func (c *Converter) Source() *XstFile { return c.src }

func fileTimeNow() uint64 {
	t := time.Now().UTC()
	return uint64(t.Unix()+11644473600)*10000000 + uint64(t.Nanosecond()/100)
}

// Open opens a source OST/PST and reads its folder list.
func (c *Converter) Open(filename string) error {
	c.Close()
	c.defaultTime = fileTimeNow()
	src, err := OpenXstFile(filename, c.status)
	if err != nil {
		return err
	}
	c.src = src
	c.srcName = filename
	return catch(func() {
		c.remapReservedFolders()
		c.loadFolderList()
	})
}

// Folder NIDs that a PST reserves for its own folders. OST files use the same
// values for ordinary folders (e.g. Sync Issues, Drafts, Journal).
var reservedFolderNIDs = []uint32{NidTopPersonalFolder, NidSearchFolderRoot, NidDeletedItems}

// remapReservedFolders gives source folders that use a reserved PST NID a new
// unused NID index, updating the folder's tables and every parent reference.
func (c *Converter) remapReservedFolders() {
	c.remapped = map[uint32]uint32{}
	if c.src.Type != FileOST {
		return
	}
	maxIndex := uint32(0)
	for _, e := range c.src.NBTs {
		if ix := nidIndex(e.Nid); ix > maxIndex {
			maxIndex = ix
		}
	}
	folderTypes := []NidType{NidNormalFolder, NidHierarchyTable, NidContentsTable, NidAssocContentsTable, NidContentsTableIndex}
	idxMap := map[uint32]uint32{}
	for _, r := range reservedFolderNIDs {
		if _, ok := c.src.nbtIndex[r]; ok {
			maxIndex++
			idxMap[nidIndex(r)] = maxIndex
		}
	}
	if len(idxMap) == 0 {
		return
	}
	for _, t := range folderTypes {
		for oldIx, newIx := range idxMap {
			c.remapped[makeNID(t, oldIx)] = makeNID(t, newIx)
		}
	}
	for i := range c.src.NBTs {
		e := &c.src.NBTs[i]
		if n, ok := c.remapped[e.Nid]; ok {
			e.Nid = n
		}
		if n, ok := c.remapped[e.NidParent]; ok && nidType(e.NidParent) == NidNormalFolder {
			e.NidParent = n
		}
	}
	c.src.nbtIndex = map[uint32]int{}
	for i, e := range c.src.NBTs {
		c.src.nbtIndex[e.Nid] = i
	}
	for _, r := range reservedFolderNIDs {
		if newIx, ok := idxMap[nidIndex(r)]; ok {
			c.status(fmt.Sprintf("folder NID %d is reserved in PST files: renumbered to %d",
				r, makeNID(NidNormalFolder, newIx)))
		}
	}
}

// mapNID returns the renumbered NID of a folder-related NID.
func (c *Converter) mapNID(nid uint32) uint32 {
	if n, ok := c.remapped[nid]; ok {
		return n
	}
	return nid
}

// Close closes the source file.
func (c *Converter) Close() {
	c.src.Close()
	c.src = nil
}

// catch converts a panic raised by the format code into an error.
func catch(fn func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok {
				err = e
			} else {
				err = fmt.Errorf("%v", r)
			}
		}
	}()
	fn()
	return nil
}

func (c *Converter) newTC() *TableContext {
	return newTableContext(func() uint32 { return c.out.nextUniqueID() })
}

func (c *Converter) replTagProps() []Property {
	id := make([]byte, 16)
	rand.Read(id)
	id[6] = id[6]&0x0f | 0x40 // GUID v4
	id[8] = id[8]&0x3f | 0x80
	c.replChange += 8
	return []Property{
		propBinary(PidTagReplItemid, PtypBinary, id),
		propU64(PidTagReplChangenum, PtypInteger64, c.replChange),
		propBinary(PidTagReplVersionHistory, PtypBinary, replVersionHistory),
	}
}

// readValidatedPC reads a PC, adds missing required properties and sorts it.
func (c *Converter) readValidatedPC(nbt NBTEntry) []Property {
	props := c.src.readPC(nbt.Nid, nbt.BidData, nbt.BidSub)
	switch nidType(nbt.Nid) {
	case NidNormalFolder:
		props = c.validateFolderPC(props)
	case NidNormalMessage, NidAssocMessage:
		props = c.validateMessagePC(props)
	}
	sortProps(props)
	return props
}

// ---------------------------------------------------------------------------
// Folder list

func (c *Converter) folderName(nbt NBTEntry) (name string) {
	err := catch(func() {
		props := c.readValidatedPC(nbt)
		if ix := findPropTyped(props, PidTagDisplayName, PtypString); ix >= 0 {
			name = decodeUTF16(props[ix].Data)
		}
	})
	if err != nil {
		c.status(fmt.Sprintf("cannot read folder NID %d: %v", nbt.Nid, err))
		return fmt.Sprintf("(unreadable folder %d)", nbt.Nid)
	}
	return name
}

// loadFolderList builds the folder tree. Parents are resolved in a second pass
// because a folder can appear in the NBT before its parent.
func (c *Converter) loadFolderList() {
	c.status("Scanning folder list....")
	c.Folders = nil
	c.Root = nil
	c.folderByNid = map[uint32]*Folder{}
	parents := map[*Folder]uint32{}
	for i, nbt := range c.src.NBTs {
		switch {
		case nbt.Nid == NidRootFolder:
			f := &Folder{NbtIndex: i, Nid: nbt.Nid}
			f.Parent = f
			c.Folders = append([]*Folder{f}, c.Folders...)
			c.Root = f
		case nidType(nbt.Nid) == NidNormalFolder || nidType(nbt.Nid) == NidSearchFolder:
			f := &Folder{NbtIndex: i, Nid: nbt.Nid, Name: c.folderName(nbt)}
			parents[f] = nbt.NidParent
			c.Folders = append(c.Folders, f)
		}
	}
	if c.Root == nil {
		panic(errors.New("root folder not found in the source file"))
	}
	for _, f := range c.Folders {
		c.folderByNid[f.Nid] = f
	}
	for _, f := range c.Folders {
		p, ok := parents[f]
		if !ok {
			continue
		}
		if parent, found := c.folderByNid[p]; found {
			f.Parent = parent
		} else {
			f.Parent = c.Root
			c.status(fmt.Sprintf("parent NID %d not found: showing folder '%s' under ROOT FOLDER", p, f.Name))
		}
	}
	// children lists for the UI, guarding against parent cycles
	for _, f := range c.Folders {
		if f.Parent != f && f.Parent != nil {
			f.Parent.Children = append(f.Parent.Children, f)
		}
	}
}

// ---------------------------------------------------------------------------
// Export

// Export converts the source into destFile. For an OST, folderNid selects the
// folder tree to export; for a PST the whole file is compacted and
// resetPassword clears the store password.
func (c *Converter) Export(destFile string, folderNid uint32, resetPassword bool) (err error) {
	if c.src == nil {
		return errors.New("no source file open")
	}
	if samePath(destFile, c.srcName) {
		return errors.New("the destination must be different from the source file")
	}
	srcName := c.srcName
	isOST := c.src.Type == FileOST
	if isOST {
		f := c.folderByNid[folderNid]
		if f == nil || f.IsRoot() {
			return errors.New("select a mailbox folder to export (not the hidden root folder)")
		}
	}
	c.skipped = 0
	defer func() {
		if c.out != nil {
			c.out.Abort()
			c.out = nil
		}
		// reopen the source so a new export starts from a clean state
		c.src.Close()
		c.src = nil
		if rerr := c.Open(srcName); rerr != nil && err == nil {
			err = rerr
		}
	}()
	if isOST {
		tmp, terr := os.CreateTemp("", "ost2pst-*.pst")
		if terr != nil {
			return terr
		}
		tmpName := tmp.Name()
		tmp.Close()
		defer func() {
			if c.src != nil && c.src.Filename == tmpName {
				c.src.Close() // Windows cannot delete an open file
				c.src = nil
			}
			os.Remove(tmpName)
		}()
		c.status("Converting OST file")
		c.status("Building temp PST file to recalculate the message sizes")
		c.status("PLEASE WAIT... this may take a while")
		if err := c.runPass(tmpName, func() { c.copySourceToPST(folderNid, filepath.Base(destFile)) }); err != nil {
			return err
		}
		c.src.Close()
		src, oerr := OpenXstFile(tmpName, c.status)
		if oerr != nil {
			return oerr
		}
		c.src = src
	}
	c.status("Building final PST file")
	if err := c.runPass(destFile, func() { c.rebuildPSTFile(resetPassword) }); err != nil {
		return err
	}
	if c.skipped > 0 {
		c.status(fmt.Sprintf("WARNING: %d item(s) could not be converted and were skipped (see log above)", c.skipped))
	}
	c.status("Export completed")
	return nil
}

func samePath(a, b string) bool {
	pa, err1 := filepath.Abs(a)
	pb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return a == b
	}
	return strings.EqualFold(filepath.Clean(pa), filepath.Clean(pb))
}

// runPass creates an output PST, runs body, writes the B-trees and closes it.
func (c *Converter) runPass(filename string, body func()) error {
	out, err := CreatePstFile(filename)
	if err != nil {
		return err
	}
	c.out = out
	c.w = newBlockWriter(out, c.src)
	if err := catch(func() {
		body()
		c.out.ExportNBT()
		c.out.ExportBBT()
		c.out.UpdateNIDHighWaterMarks()
	}); err != nil {
		return err
	}
	err = c.out.Close()
	c.LastOutput = c.out.summary()
	c.out = nil
	return err
}

// summary describes the PST file.
func (p *PstFile) summary() string {
	return fmt.Sprintf("Filename   : %s\nFile Type  : PST\nFile Size  : %d kB\nFree area  : %d kB\nEncryption : %s\nNr of NBTs : %d\nNr of BBTs : %d",
		p.Filename, p.Header.Root.FileEOF/1024, p.Header.Root.AMapFree/1024,
		cryptName(p.Header.CryptMethod), len(p.NBTs), len(p.BBTs))
}

// item runs fn for one object; failures are logged and the object skipped.
func (c *Converter) item(nid uint32, fn func()) {
	if err := catch(fn); err != nil {
		c.skipped++
		c.status(fmt.Sprintf("skipped NID %d: %v", nid, err))
	}
}

// --- pass 1: OST -> temporary PST ------------------------------------------

func (c *Converter) copySourceToPST(folderNid uint32, displayName string) {
	exp := c.checkFoldersToExport(folderNid)
	c.buildPSTObjects(exp, displayName)
	n := len(c.src.NBTs)
	c.status("")
	for i, nbt := range c.src.NBTs {
		if i%64 == 0 || i == n-1 {
			c.rep.StatusReplace(fmt.Sprintf("Converting OST NBT entry %d of %d", i+1, n))
			c.rep.Progress(i+1, n, "Converting OST")
		}
		if !c.toBeExported(nbt) {
			continue
		}
		c.item(nbt.Nid, func() {
			switch {
			case nidType(nbt.Nid) == NidNormalMessage:
				c.rebuildMessageObject(nbt)
			case isPC(nbt.Nid):
				c.rebuildPC(nbt)
			case isTC(nbt.Nid):
				c.rebuildTC(nbt)
			default:
				c.copyNDB(nbt)
				c.fixContentTableIndex(nbt)
			}
		})
	}
}

func (c *Converter) checkFoldersToExport(folderNid uint32) *Folder {
	c.loadFolderList()
	f := c.folderByNid[folderNid]
	if f == nil {
		panic(fmt.Errorf("selected folder %d not found in the source file", folderNid))
	}
	c.markSubfolders(f)
	nbt := &c.src.NBTs[f.NbtIndex]
	nbt.NidParent = NidTopPersonalFolder
	f.Properties = c.src.readPC(nbt.Nid, nbt.BidData, nbt.BidSub)
	return f
}

func (c *Converter) markSubfolders(f *Folder) {
	if f.ToBeExported {
		return // already visited (protects against parent/child cycles)
	}
	f.ToBeExported = true
	if f.Name == "IPM_COMMON_VIEWS" || f.IsRoot() {
		return
	}
	for _, ch := range f.Children {
		c.markSubfolders(ch)
	}
}

func (c *Converter) folderExported(nid uint32) bool {
	f := c.folderByNid[nid]
	return f != nil && f.ToBeExported
}

func (c *Converter) toBeExported(nbt NBTEntry) bool {
	if nbt.Nid == 34666 || nbt.BidData == 0 || c.out.hasNBT(nbt.Nid) {
		return false
	}
	switch nidType(nbt.Nid) {
	case NidNormalFolder, NidHierarchyTable, NidContentsTable, NidAssocContentsTable, NidContentsTableIndex:
		return c.folderExported(changeNidType(nbt.Nid, NidNormalFolder))
	case NidNormalMessage, NidAssocMessage:
		return c.folderExported(nbt.NidParent)
	}
	return nbt.Nid == NidNameToIDMap || nbt.Nid == 0x06B6
}

func isPC(nid uint32) bool {
	if nidType(nid) == NidInternal {
		return nid == 0x21 || nid == 0x61 || nid == 0x122
	}
	switch nidType(nid) {
	case NidNormalFolder, NidSearchFolder, NidNormalMessage, NidAttachment, NidAssocMessage:
		return true
	}
	return false
}

func isTC(nid uint32) bool {
	if nidType(nid) == NidInternal {
		return nid == 0xA1 || nid == 0xC1
	}
	switch nidType(nid) {
	case NidHierarchyTable, NidContentsTable, NidAssocContentsTable, NidSearchContentsTable,
		NidAttachmentTable, NidRecipientTable, 22:
		return true
	}
	return false
}

func (c *Converter) fixContentTableIndex(nbt NBTEntry) {
	// SCANPST expects rgnid[3] = rgnid[19] when a CONTENTS_TABLE_INDEX is present
	if nidType(nbt.Nid) == NidContentsTableIndex {
		c.out.Header.Rgnid[3] = c.out.Header.Rgnid[19]
	}
}

func (c *Converter) copyNDB(nbt NBTEntry) {
	data := c.w.copyDatablock(nbt.BidData)
	sub := uint64(0)
	if nbt.BidSub > 0 {
		sub = c.w.copySubnode(nbt.BidSub, nil).Bid
	}
	c.out.addNBT(NBTEntry{Nid: nbt.Nid, BidData: data.Bid, BidSub: sub, NidParent: nbt.NidParent})
}

func (c *Converter) addNDB(nbt NBTEntry, blocks [][]byte, sub uint64) {
	r := c.w.addDatablocks(blocks, nbt.BidData)
	c.out.addNBT(NBTEntry{Nid: nbt.Nid, BidData: r.Bid, BidSub: sub, NidParent: nbt.NidParent})
}

func (c *Converter) rebuildPC(nbt NBTEntry) {
	props := c.readValidatedPC(nbt)
	b := newPCBTH(c.w, props)
	sub := uint64(0)
	if nbt.BidSub != 0 {
		sub = c.w.copySubnode(nbt.BidSub, nil).Bid
	}
	c.addNDB(nbt, b.heapBlocks(bTypePC), sub)
}

func (c *Converter) rebuildTC(nbt NBTEntry) {
	tc := c.readTCFromPCs(nbt)
	switch nidType(nbt.Nid) {
	case NidContentsTable, NidAssocContentsTable:
		requireCols(tc, contentsRequiredCols)
	case NidHierarchyTable:
		requireCols(tc, hierarchyRequiredCols)
	}
	b := newTCBTH(c.w, tc, true)
	sub := uint64(0)
	if len(b.subnodes) > 0 {
		sub = c.w.addSLEntries(b.subnodes).Bid
	}
	c.addNDB(nbt, b.heapBlocks(bTypeTC), sub)
}

// rebuildMessageObject converts a message with its recipients and attachments.
func (c *Converter) rebuildMessageObject(nbt NBTEntry) {
	var newSubs []SLEntry
	for _, sn := range c.src.getSLEntries(nbt.BidSub) {
		c.out.updateSubnodeNIDHighWaterMark(uint32(sn.Nid))
		var n SLEntry
		switch nidType(uint32(sn.Nid)) {
		case NidRecipientTable, NidAttachmentTable:
			n = c.rebuildSubnodeTC(sn, false)
		case 0x16: // undocumented TC present in message objects
			n = c.rebuildSubnodeTC(sn, true)
		case NidAttachment:
			n = c.rebuildAttachment(sn)
		case NidLTP:
			n = c.w.copySLEntry(sn)
		default:
			panic(fmt.Errorf("message %d has an invalid subnode nid type %d", nbt.Nid, sn.Nid))
		}
		newSubs = append(newSubs, n)
	}
	sub := c.w.addSLEntries(newSubs)
	b := newPCBTH(c.w, c.readValidatedPC(nbt))
	blocks := b.heapBlocks(bTypePC)
	c.addNDBData(nbt.Nid, blocks, sub.Bid, nbt.NidParent)
}

// rebuildAttachedMessage converts a message embedded in an attachment and
// returns its new data/subnode bids.
func (c *Converter) rebuildAttachedMessage(nbt NBTEntry, depth int) NBTEntry {
	if depth > 16 {
		panic(fmt.Errorf("embedded messages nested too deep"))
	}
	var newSubs []SLEntry
	for _, sn := range c.src.getSLEntries(nbt.BidSub) {
		c.out.updateSubnodeNIDHighWaterMark(uint32(sn.Nid))
		var n SLEntry
		switch nidType(uint32(sn.Nid)) {
		case NidRecipientTable:
			n = c.rebuildSubnodeTC(sn, false)
		case NidAttachmentTable:
			n = c.rebuildAttachmentTableTC(sn)
		case NidAttachment:
			n = c.rebuildAttachmentDepth(sn, depth)
		default:
			n = c.w.copySLEntry(sn)
		}
		newSubs = append(newSubs, n)
	}
	sub := c.w.addSLEntries(newSubs)
	b := newPCBTH(c.w, c.readValidatedPC(nbt))
	blocks := b.heapBlocks(bTypePC)
	var r BREF
	if len(blocks) == 1 {
		r = c.w.addDatablock(blocks[0], 0)
	} else {
		r = c.w.addDataTree(blocks)
	}
	return NBTEntry{Nid: nbt.Nid, BidData: r.Bid, BidSub: sub.Bid, NidParent: nbt.NidParent}
}

func (c *Converter) rebuildAttachment(sn SLEntry) SLEntry { return c.rebuildAttachmentDepth(sn, 0) }

func (c *Converter) rebuildAttachmentDepth(sn SLEntry, depth int) SLEntry {
	props := c.src.readPC(uint32(sn.Nid), sn.BidData, sn.BidSub)
	ix := findPropTyped(props, PidTagAttachDataBinary, PtypObject)
	if ix >= 0 && len(props[ix].Data) >= 4 {
		objNid := le32(props[ix].Data, 0)
		if nidType(objNid) == NidNormalMessage { // the attachment is a message ([MS-PST] 2.4.6.2.2)
			subnodes := c.src.getSLEntries(sn.BidSub)
			si := findSubnode(subnodes, objNid)
			if si < 0 {
				panic(fmt.Errorf("attachment message object %d subnode not found", objNid))
			}
			msgSL := subnodes[si]
			c.out.updateSubnodeNIDHighWaterMark(objNid)
			nn := c.rebuildAttachedMessage(NBTEntry{Nid: objNid, BidData: msgSL.BidData, BidSub: msgSL.BidSub}, depth+1)
			msgSL.BidData = nn.BidData
			msgSL.BidSub = nn.BidSub
			slRef := c.w.copySubnodes(subnodes, &msgSL)
			b := newPCBTH(c.w, props)
			data := c.w.addDatablocks(b.heapBlocks(bTypePC), 0)
			return SLEntry{Nid: sn.Nid, BidData: data.Bid, BidSub: slRef.Bid}
		}
	}
	sub := uint64(0)
	if sn.BidSub > 0 {
		sub = c.w.copySubnode(sn.BidSub, nil).Bid
	}
	b := newPCBTH(c.w, c.src.readPC(uint32(sn.Nid), sn.BidData, sn.BidSub))
	data := c.w.addDatablocks(b.heapBlocks(bTypePC), sn.BidData)
	return SLEntry{Nid: sn.Nid, BidData: data.Bid, BidSub: sub}
}

func (c *Converter) rebuildAttachmentTableTC(sn SLEntry) SLEntry {
	tc := c.readTCWithRowData(uint32(sn.Nid), sn.BidData, sn.BidSub, false)
	b := newTCBTH(c.w, tc, true)
	n := SLEntry{Nid: sn.Nid}
	if len(b.subnodes) > 0 {
		n.BidSub = c.w.addSLEntries(b.subnodes).Bid
	}
	n.BidData = c.w.addDatablocks(b.heapBlocks(bTypeTC), 0).Bid
	return n
}

// rebuildSubnodeTC rebuilds a recipient/attachment TC kept in a subnode; the
// row matrix subnode is rewritten and the other subnodes are copied.
func (c *Converter) rebuildSubnodeTC(sn SLEntry, refreshIndex bool) SLEntry {
	tc := c.readTCWithRowData(uint32(sn.Nid), sn.BidData, sn.BidSub, refreshIndex)
	b := newTCBTH(c.w, tc, false)
	sub := uint64(0)
	if sn.BidSub > 0 {
		var pstSN []SLEntry
		for _, e := range c.src.getSLEntries(sn.BidSub) {
			var data [][]byte
			if e.Nid == uint64(uint32(tc.info.hnidRows)) {
				data = tc.rowMatrixBlocks()
			} else {
				data = [][]byte{c.src.readFullDatablock(e.BidData)}
			}
			n := SLEntry{Nid: e.Nid, BidData: c.w.addDatablocks(data, e.BidData).Bid}
			if e.BidSub > 0 {
				n.BidSub = c.w.copySubnode(e.BidSub, nil).Bid
			}
			c.out.updateSubnodeNIDHighWaterMark(uint32(e.Nid))
			pstSN = append(pstSN, n)
		}
		sub = c.w.addSLEntries(pstSN).Bid
	}
	data := c.w.addDatablocks(b.heapBlocks(bTypeTC), sn.BidData)
	return SLEntry{Nid: sn.Nid, BidData: data.Bid, BidSub: sub}
}

// --- pass 2: refresh message sizes ------------------------------------------

func (c *Converter) rebuildPSTFile(resetPassword bool) {
	c.messageSizes()
	n := len(c.src.NBTs)
	c.status(fmt.Sprintf("Exporting NBT data (%d entries)", n))
	c.status("")
	for i, nbt := range c.src.NBTs {
		if i%64 == 0 || i == n-1 {
			c.rep.StatusReplace(fmt.Sprintf("Exporting NBT entry %d of %d", i+1, n))
			c.rep.Progress(i+1, n, "Rebuilding PST")
		}
		c.item(nbt.Nid, func() {
			switch {
			case nidType(nbt.Nid) == NidNormalMessage:
				c.refreshMessagePC(nbt)
			case nidType(nbt.Nid) == NidContentsTable:
				c.refreshContentsTable(nbt)
			case nbt.BidData == 0:
				c.out.addNBT(nbt)
			case resetPassword && nbt.Nid == NidMessageStore:
				c.resetPasswordPC(nbt)
			default:
				c.copyNDB(nbt)
				c.fixContentTableIndex(nbt)
			}
		})
	}
}

func (c *Converter) messageSizes() {
	c.msgSizes = map[uint32]uint32{}
	var msgs []NBTEntry
	for _, nbt := range c.src.NBTs {
		if nidType(nbt.Nid) == NidNormalMessage {
			msgs = append(msgs, nbt)
		}
	}
	c.status(fmt.Sprintf("Reading PST message sizes (%d messages)", len(msgs)))
	for i, nbt := range msgs {
		nbt := nbt
		c.item(nbt.Nid, func() {
			size := c.blockDataSize(nbt.BidData)
			if nbt.BidSub > 0 {
				size += c.subnodeDataSize(nbt.BidSub, 0)
			}
			c.msgSizes[nbt.Nid] = size
		})
		if i%64 == 0 || i == len(msgs)-1 {
			c.rep.Progress(i+1, len(msgs), "Calculating sizes")
		}
	}
}

func (c *Converter) subnodeDataSize(bidSub uint64, depth int) uint32 {
	if depth > 16 {
		return 0
	}
	size := uint32(0)
	for _, s := range c.src.getSLEntries(bidSub) {
		size += c.blockDataSize(s.BidData)
		if s.BidSub > 0 {
			size += c.subnodeDataSize(s.BidSub, depth+1)
		}
	}
	return size
}

func (c *Converter) blockDataSize(bid uint64) uint32 {
	ix, ok := c.src.bbtIndex[bid]
	if !ok {
		return 0
	}
	b := &c.src.BBTs[ix]
	if b.Type == bTypeD {
		return uint32(b.CbInflated)
	}
	return b.IcbTotal
}

func (c *Converter) refreshMessagePC(nbt NBTEntry) {
	props := c.src.readPC(nbt.Nid, nbt.BidData, nbt.BidSub)
	ix := findProp(props, PidTagMessageSize)
	size, ok := c.msgSizes[nbt.Nid]
	if !ok || ix < 0 {
		panic(fmt.Errorf("missing message size for nid %d", nbt.Nid))
	}
	if le32(props[ix].Data, 0) == size {
		c.copyNDB(nbt) // size already correct
		return
	}
	props[ix] = propFromHNID(props[ix].ID, props[ix].Type, HNID(size), c.defaultTime)
	b := newPCBTH(c.w, props)
	sub := uint64(0)
	if nbt.BidSub != 0 {
		sub = c.w.copySubnode(nbt.BidSub, nil).Bid
	}
	c.addNDB(nbt, b.heapBlocks(bTypePC), sub)
}

func (c *Converter) refreshContentsTable(nbt NBTEntry) {
	tc := c.readTCWithRowData(nbt.Nid, nbt.BidData, nbt.BidSub, true)
	changed := false
	for i, ri := range tc.rowIndexes {
		size, ok := c.msgSizes[ri.rowID]
		if !ok || i >= len(tc.rows) {
			continue
		}
		rd := tc.rows[i]
		ix := findProp(rd.props, PidTagMessageSize)
		if ix < 0 || len(rd.props[ix].Data) < 4 {
			continue
		}
		if le32(rd.props[ix].Data, 0) != size {
			changed = true
			rd.props[ix] = propFromHNID(rd.props[ix].ID, rd.props[ix].Type, HNID(size), c.defaultTime)
		}
	}
	if !changed {
		c.copyNDB(nbt)
		return
	}
	b := newTCBTH(c.w, tc, true)
	sub := uint64(0)
	if len(b.subnodes) > 0 {
		sub = c.w.addSLEntries(b.subnodes).Bid
	}
	c.addNDB(nbt, b.heapBlocks(bTypeTC), sub)
}

func (c *Converter) resetPasswordPC(nbt NBTEntry) {
	props := c.src.readPC(nbt.Nid, nbt.BidData, nbt.BidSub)
	ix := findProp(props, PidTagPstPassword)
	if ix < 0 { // no password set: copy the store unchanged
		c.copyNDB(nbt)
		return
	}
	props[ix] = propFromHNID(PidTagPstPassword, PtypInteger32, 0, c.defaultTime)
	b := newPCBTH(c.w, props)
	c.addNDB(nbt, b.heapBlocks(bTypePC), 0)
	c.status("Password reset")
}

// MailboxTop returns the folder holding the user's mail (the IPM subtree the
// message store points to), or nil when it cannot be determined.
func (c *Converter) MailboxTop() (top *Folder) {
	if c.src == nil {
		return nil
	}
	catch(func() {
		ix, ok := c.src.nbtIndex[NidMessageStore]
		if !ok {
			return
		}
		e := c.src.NBTs[ix]
		props := c.src.readPC(e.Nid, e.BidData, e.BidSub)
		if p := findProp(props, PidTagIpmSubTreeEntryId); p >= 0 && len(props[p].Data) >= 24 {
			nid := c.mapNID(le32(props[p].Data, len(props[p].Data)-4))
			if f := c.folderByNid[nid]; f != nil && !f.IsRoot() {
				top = f
			}
		}
	})
	if top != nil || c.Root == nil {
		return top
	}
	// Folder names are localized ("Root - Mailbox" is "Hoofdmap - Postvak" in
	// Dutch), so pick the candidate whose subtree holds the most messages.
	// IPM_SUBTREE is an internal name and is not translated.
	perFolder := map[uint32]int{}
	for _, e := range c.src.NBTs {
		if t := nidType(e.Nid); t == NidNormalMessage {
			perFolder[e.NidParent]++
		}
	}
	var count func(f *Folder, depth int) int
	count = func(f *Folder, depth int) int {
		if depth > 64 {
			return 0
		}
		n := perFolder[f.Nid]
		for _, ch := range f.Children {
			n += count(ch, depth+1)
		}
		return n
	}
	best, bestN := (*Folder)(nil), -1
	for _, f := range c.Folders {
		if f.IsRoot() {
			continue
		}
		cand := strings.EqualFold(f.Name, "IPM_SUBTREE") ||
			(f.Parent == c.Root && (strings.EqualFold(f.Name, "Top of Information Store") || strings.EqualFold(f.Name, "Top of Outlook data file")))
		if !cand {
			continue
		}
		if n := count(f, 0); n > bestN {
			best, bestN = f, n
		}
	}
	return best
}

// FolderByNID returns the folder with the given NID.
func (c *Converter) FolderByNID(nid uint32) *Folder { return c.folderByNid[nid] }
