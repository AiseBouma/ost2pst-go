package pst

import (
	"fmt"
	"os"
	"sort"
)

// PstFile is the output PST (Unicode, permute encryption).
type PstFile struct {
	Filename string
	f        *os.File
	Header   Header
	NBTs     []NBTEntry
	BBTs     []BBTEntry
	nbtSet   map[uint32]struct{}
	bbtIndex map[uint64]int

	// AMap state. Blocks are only ever appended, so the free space of the last
	// AMap is always a contiguous run at its end.
	amapCount    int
	lastAMapFree uint32 // free 64-byte slots on the last AMap
	curAMap      []byte // 512-byte page of the last AMap, kept in memory
	curAMapUsed  int    // allocated slots on the last AMap
	curAMapDirty bool
	gaps         []gapBlock
	dlistPage    []byte
	fileEnd      uint64
}

type gapBlock struct {
	ib uint64
	cb uint32
}

// CreatePstFile creates (truncates) a new PST file.
func CreatePstFile(filename string) (*PstFile, error) {
	f, err := os.Create(filename)
	if err != nil {
		return nil, fmt.Errorf("ERROR CREATING PST: %w", err)
	}
	p := &PstFile{Filename: filename, f: f, nbtSet: map[uint32]struct{}{}, bbtIndex: map[uint64]int{}}
	if err := p.init(); err != nil {
		f.Close()
		return nil, fmt.Errorf("ERROR CREATING PST: %w", err)
	}
	return p, nil
}

func (p *PstFile) init() error {
	p.initializeHeader()
	// header, then zeros up to the DList page at 0x4200
	if _, err := p.f.WriteAt(make([]byte, DListOffset), 0); err != nil {
		return err
	}
	p.fileEnd = DListOffset
	if err := p.writeHeader(); err != nil {
		return err
	}
	// empty DList page
	p.dlistPage = make([]byte, 512)
	p.setPageTrailer(p.dlistPage, ptypeDL, DListOffset, 0)
	p.writeAt(p.dlistPage, DListOffset)
	return nil
}

func (p *PstFile) initializeHeader() {
	h := &p.Header
	h.Magic = Magic
	h.MagicClient = MagicClient
	h.Ver = MinUnicodeVer
	h.VerClient = VerClient
	h.PlatformCreate = 1
	h.PlatformAccess = 1
	h.BidNextP = 1
	h.Unique = 1
	h.Root.AMapValid = 0x02
	h.Sentinel = 0x80
	h.CryptMethod = CryptPermute
	h.BidNextB = 4
	for i := range h.RgbFM {
		h.RgbFM[i] = 0xff
		h.RgbFP[i] = 0xff
	}
	for i := range h.Rgnid {
		h.Rgnid[i] = 0x400
		switch NidType(i) {
		case NidSearchFolder:
			h.Rgnid[i] = 0x4000
		case NidNormalMessage:
			h.Rgnid[i] = 0x10000
		case NidAssocMessage:
			h.Rgnid[i] = 0x8000
		}
	}
}

func (p *PstFile) writeAt(b []byte, off uint64) {
	if _, err := p.f.WriteAt(b, int64(off)); err != nil {
		panic(fmt.Errorf("PST WRITE ERROR at %d: %w", off, err))
	}
	if end := off + uint64(len(b)); end > p.fileEnd {
		p.fileEnd = end
	}
}

func (p *PstFile) writeHeader() error {
	h := &p.Header
	h.Root.FileEOF = p.fileEnd
	b := h.marshal()
	h.CRCPartial = computeCRC(b[8 : 8+471])
	h.CRCFull = computeCRC(b[8 : 8+516])
	b = h.marshal()
	_, err := p.f.WriteAt(b, 0)
	return err
}

// Close finalises the PST (gap release, DList, header) and closes it.
func (p *PstFile) Close() error {
	if p.f == nil {
		return nil
	}
	p.flushAMap()
	p.releaseGapBlocks()
	// DList stays empty; refresh its CRC
	put32(p.dlistPage, 496+4, computeCRC(p.dlistPage[:AMapPageEntryBytes]))
	p.writeAt(p.dlistPage, DListOffset)
	err := p.writeHeader()
	if cerr := p.f.Close(); err == nil {
		err = cerr
	}
	p.f = nil
	return err
}

// Abort closes the file without finalising it.
func (p *PstFile) Abort() {
	if p.f != nil {
		p.f.Close()
		p.f = nil
	}
}

func (p *PstFile) hasNBT(nid uint32) bool {
	_, ok := p.nbtSet[nid]
	return ok
}

func (p *PstFile) addNBT(e NBTEntry) {
	p.NBTs = append(p.NBTs, e)
	p.nbtSet[e.Nid] = struct{}{}
}

func (p *PstFile) incrementRefCount(bid uint64) {
	if bid == 0 {
		return
	}
	ix, ok := p.bbtIndex[bid]
	if !ok {
		panic(fmt.Errorf("block %d not found in output BBT", bid))
	}
	p.BBTs[ix].CRef++
}

// nextUniqueID returns the next dwUnique value (used for LtpRowVer).
func (p *PstFile) nextUniqueID() uint32 {
	v := p.Header.Unique
	p.Header.Unique++
	return v
}

func (p *PstFile) updateNIDHighWaterMark(nid uint32) {
	t := nid % 32
	if ix := nid >> 5; ix > p.Header.Rgnid[t] {
		p.Header.Rgnid[t] = ix
	}
}

// updateSubnodeNIDHighWaterMark mirrors what SCANPST checks for subnode nids,
// although [MS-PST] 2.6.1.2.2 says subnode nids are not allocated from rgnid.
func (p *PstFile) updateSubnodeNIDHighWaterMark(nid uint32) {
	switch nid % 32 {
	case 0, 1, 3, 4, 5, 7, 8, 9, 13, 18, 19, 20, 21, 22, 24, 27, 28, 29, 31:
		p.updateNIDHighWaterMark(nid)
	}
}

// UpdateNIDHighWaterMarks raises rgnid for every NBT entry.
func (p *PstFile) UpdateNIDHighWaterMarks() {
	for _, e := range p.NBTs {
		p.updateNIDHighWaterMark(e.Nid)
	}
}

// newDataBlock allocates a block of cb data bytes and registers it in the BBT.
func (p *PstFile) newDataBlock(cb int, internal bool) BREF {
	if cb > MaxDatablockSize {
		panic(fmt.Errorf("DATA BLOCK ERROR: %d exceeds %d bytes", cb, MaxDatablockSize))
	}
	e := BBTEntry{Cb: uint16(cb), CRef: 2}
	e.Ib = p.newBlockOffset(uint32(blockTotalSize(cb)), false)
	e.Bid = p.nextDataBid()
	if internal {
		e.Bid |= 2
	}
	p.bbtIndex[e.Bid] = len(p.BBTs)
	p.BBTs = append(p.BBTs, e)
	return e.BREF
}

func (p *PstFile) nextDataBid() uint64 {
	b := p.Header.BidNextB
	p.Header.BidNextB += 4
	return b
}

func (p *PstFile) nextPageBid() uint64 {
	b := p.Header.BidNextP
	p.Header.BidNextP++
	return b
}

// writeBlock encodes (external blocks), adds the trailer and writes a block.
func (p *PstFile) writeBlock(ref BREF, data []byte) {
	d := make([]byte, blockTotalSize(len(data)))
	copy(d, data)
	if ref.Bid&0x02 == 0 {
		cryptPermuteEncode(d[:len(data)])
	}
	t := len(d) - blockTrailerSize
	put16(d, t, uint16(len(data)))
	put16(d, t+2, computeSig(ref.Ib, ref.Bid))
	put32(d, t+4, computeCRC(d[:len(data)]))
	put64(d, t+8, ref.Bid)
	p.writeAt(d, ref.Ib)
}

// ---------------------------------------------------------------------------
// AMap management

func (p *PstFile) newBlockOffset(size uint32, align512 bool) uint64 {
	if size > MaxBlockSize {
		panic(fmt.Errorf("data block request for %d bytes failed, max is %d", size, MaxBlockSize))
	}
	for {
		if p.amapCount > 0 {
			offset := p.Header.Root.AMapLast + uint64(p.curAMapUsed)*64
			gap := uint32(0)
			if align512 && offset%0x200 != 0 {
				gap = uint32(0x200 - offset%0x200)
			}
			if p.lastAMapFree*64 >= size+gap {
				if gap > 0 {
					p.allocateOnLastAMap(gap) // released again before closing
					p.gaps = append(p.gaps, gapBlock{ib: offset, cb: gap})
					offset += uint64(gap)
				}
				p.allocateOnLastAMap(size)
				return offset
			}
		}
		p.newAMap()
	}
}

func (p *PstFile) allocateOnLastAMap(size uint32) {
	slots := int(size / 64)
	for i := 0; i < slots; i++ {
		setBit(p.curAMap, p.curAMapUsed+i)
	}
	p.curAMapUsed += slots
	p.lastAMapFree -= uint32(slots)
	p.Header.Root.AMapFree -= uint64(size)
	p.curAMapDirty = true
}

func (p *PstFile) flushAMap() {
	if p.curAMap == nil || !p.curAMapDirty {
		return
	}
	put32(p.curAMap, 496+4, computeCRC(p.curAMap[:AMapPageEntryBytes]))
	p.writeAt(p.curAMap, p.Header.Root.AMapLast)
	p.curAMapDirty = false
}

func (p *PstFile) newAMap() {
	p.flushAMap()
	if p.Header.Root.AMapLast == 0 {
		p.Header.Root.AMapLast = FirstAMapOffset
	} else {
		p.Header.Root.AMapLast += AMapAllocationSize
	}
	base := p.Header.Root.AMapLast
	pmap, fmap, fpmap := false, false, false
	used := 8               // the AMap page itself
	if p.amapCount%8 == 0 { // PMap every 8 AMaps
		pmap = true
		used += 8
	}
	if p.amapCount >= 128 && (p.amapCount-128)%496 == 0 {
		fmap = true
		used += 8
	}
	if p.amapCount >= 8192 && (p.amapCount-8192)%31744 == 0 {
		fpmap = true
		used += 8
	}
	p.amapCount++
	// reserve the whole AMap range (zero filled)
	end := base + AMapAllocationSize
	if err := p.f.Truncate(int64(end)); err != nil {
		panic(fmt.Errorf("PST WRITE ERROR: %w", err))
	}
	if end > p.fileEnd {
		p.fileEnd = end
	}
	p.curAMap = make([]byte, 512)
	for i := 0; i < used; i++ {
		setBit(p.curAMap, i)
	}
	p.curAMapUsed = used
	p.lastAMapFree = uint32(AMapPageEntryBytes*8 - used)
	p.setPageTrailer(p.curAMap, ptypeAMap, base, 0)
	p.writeAt(p.curAMap, base)
	off := base
	for _, m := range []struct {
		on    bool
		ptype byte
	}{{pmap, ptypePMap}, {fmap, ptypeFMap}, {fpmap, ptypeFPMap}} {
		if !m.on {
			continue
		}
		off += 512
		page := make([]byte, 512)
		for i := 0; i < AMapPageEntryBytes; i++ {
			page[i] = 0xff
		}
		p.setPageTrailer(page, m.ptype, off, 0)
		p.writeAt(page, off)
	}
	p.Header.Root.AMapFree += uint64(p.lastAMapFree) * 64
}

func (p *PstFile) releaseGapBlocks() {
	if len(p.gaps) == 0 {
		return
	}
	pages := map[uint64][]byte{}
	for _, g := range p.gaps {
		amap := FirstAMapOffset + (g.ib-FirstAMapOffset)/AMapAllocationSize*AMapAllocationSize
		page, ok := pages[amap]
		if !ok {
			page = make([]byte, 512)
			if _, err := p.f.ReadAt(page, int64(amap)); err != nil {
				panic(fmt.Errorf("PST READ ERROR: %w", err))
			}
			pages[amap] = page
		}
		first := int((g.ib - amap) / 64)
		for i := 0; i < int(g.cb/64); i++ {
			clearBit(page, first+i)
		}
		p.lastAMapFree += g.cb / 64
		p.Header.Root.AMapFree += uint64(g.cb)
	}
	offs := make([]uint64, 0, len(pages))
	for o := range pages {
		offs = append(offs, o)
	}
	sort.Slice(offs, func(i, j int) bool { return offs[i] < offs[j] })
	for _, o := range offs {
		page := pages[o]
		put32(page, 496+4, computeCRC(page[:AMapPageEntryBytes]))
		p.writeAt(page, o)
	}
}

// setPageTrailer fills the 16-byte PAGETRAILER of a 512-byte page.
func (p *PstFile) setPageTrailer(page []byte, ptype byte, offset, bid uint64) {
	t := 496
	page[t] = ptype
	page[t+1] = ptype
	put16(page, t+2, 0)
	put64(page, t+8, offset)
	if ptype == ptypeBBT || ptype == ptypeNBT || ptype == ptypeDL {
		if bid == 0 {
			bid = p.nextPageBid()
		}
		put64(page, t+8, bid)
		put16(page, t+2, computeSig(offset, bid))
	}
	put32(page, t+4, computeCRC(page[:AMapPageEntryBytes]))
}

func (p *PstFile) newPageBlock() BREF {
	ib := p.newBlockOffset(NodePageBlockSize, true) // pages must be 0x200 aligned
	return BREF{Ib: ib, Bid: p.nextPageBid()}
}

func (p *PstFile) writeBTPage(entries [][]byte, cbEnt int, ptype byte, level int) BREF {
	page := make([]byte, 512)
	for i, e := range entries {
		copy(page[i*cbEnt:], e)
	}
	page[488] = byte(len(entries))
	page[489] = byte(BTPageEntryBytes / cbEnt)
	page[490] = byte(cbEnt)
	page[491] = byte(level)
	ref := p.newPageBlock()
	p.setPageTrailer(page, ptype, ref.Ib, ref.Bid)
	p.writeAt(page, ref.Ib)
	return ref
}
