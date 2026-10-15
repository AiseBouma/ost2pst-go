package pst

import (
	"bytes"
	"compress/flate"
	"fmt"
	"io"
	"os"
)

// XstFile is a source OST or PST file (Unicode only).
type XstFile struct {
	Filename string
	f        *os.File
	Header   Header
	Type     FileType
	NBTs     []NBTEntry
	BBTs     []srcBlock
	nbtIndex map[uint32]int
	bbtIndex map[uint64]int
	cleanBID bool
	warn     func(string)
}

// OpenXstFile opens an OST/PST and reads its node and block B-trees.
func OpenXstFile(filename string, warn func(string)) (x *XstFile, err error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("ERROR OPENING OST/PST: %w", err)
	}
	x = &XstFile{Filename: filename, f: f, cleanBID: true, warn: warn,
		nbtIndex: map[uint32]int{}, bbtIndex: map[uint64]int{}}
	defer func() {
		if r := recover(); r != nil {
			f.Close()
			x, err = nil, fmt.Errorf("ERROR OPENING OST/PST: %v", r)
		}
	}()
	hb := make([]byte, HeaderSize)
	if _, err := f.ReadAt(hb, 0); err != nil {
		panic(fmt.Errorf("cannot read header: %w", err))
	}
	x.Header = unmarshalHeader(hb)
	if x.Header.Magic != Magic {
		panic(fmt.Errorf("%s is not a PST/OST file (wrong magic key)", filename))
	}
	if x.Header.Ver < MinUnicodeVer {
		panic(fmt.Errorf("%s is not a UNICODE PST/OST file", filename))
	}
	if x.Header.CryptMethod == CryptCyclic {
		panic(fmt.Errorf("%s uses NDB_CRYPT_CYCLIC (high) encryption, which is not supported", filename))
	}
	if x.Header.Ver == OSTVersion {
		x.Type = FileOST
	} else {
		x.Type = FilePST
	}
	x.readBTree(x.Header.Root.NBT.Ib, 0)
	x.readBTree(x.Header.Root.BBT.Ib, 0)
	for i, e := range x.NBTs {
		x.nbtIndex[e.Nid] = i
	}
	return x, nil
}

// Close closes the file.
func (x *XstFile) Close() {
	if x != nil && x.f != nil {
		x.f.Close()
		x.f = nil
	}
}

func (x *XstFile) is4K() bool { return x.Type == FileOST }

func (x *XstFile) cleanBid(bid uint64) uint64 {
	if x.cleanBID { // the a-bit should be 0, OST files sometimes have 1
		return bid &^ 1
	}
	return bid
}

func (x *XstFile) readAt(b []byte, off uint64) {
	if _, err := x.f.ReadAt(b, int64(off)); err != nil && err != io.EOF {
		panic(fmt.Errorf("read error at offset %d: %w", off, err))
	} else if err == io.EOF {
		panic(fmt.Errorf("unexpected end of file at offset %d", off))
	}
}

func (x *XstFile) readBTree(offset uint64, depth int) {
	if depth > 16 {
		panic(fmt.Errorf("B-tree too deep (corrupt file?)"))
	}
	var page []byte
	var cEnt, cbEnt, cLevel int
	var ptype byte
	if x.Type == FileOST {
		page = make([]byte, 4096)
		x.readAt(page, offset)
		cEnt = int(le16(page, 4056))
		cbEnt = int(page[4060])
		cLevel = int(page[4061])
		ptype = page[4072]
	} else {
		page = make([]byte, 512)
		x.readAt(page, offset)
		cEnt = int(page[488])
		cbEnt = int(page[490])
		cLevel = int(page[491])
		ptype = page[496]
	}
	entryBytes := BTPageEntryBytes
	if x.Type == FileOST {
		entryBytes = BTPageEntryBytesOST
	}
	for i := 0; i < cEnt; i++ {
		o := i * cbEnt
		if o+cbEnt > entryBytes || cbEnt == 0 {
			panic(fmt.Errorf("B-tree page entry out of range at offset %d", offset))
		}
		e := page[o : o+cbEnt]
		if cLevel > 0 {
			x.readBTree(le64(e, 16), depth+1) // BTENTRY.BREF.ib
			continue
		}
		switch ptype {
		case ptypeNBT:
			n := readNBTEntry(e)
			n.BidData = x.cleanBid(n.BidData)
			n.BidSub = x.cleanBid(n.BidSub)
			x.NBTs = append(x.NBTs, n)
		case ptypeBBT:
			b := srcBlock{BREF: BREF{Bid: x.cleanBid(le64(e, 0)), Ib: le64(e, 8)}}
			if x.Type == FileOST {
				b.CbStored = le16(e, 16)
				b.CbInflated = le16(e, 18)
				b.CRef = le16(e, 20)
			} else {
				b.CbStored = le16(e, 16)
				b.CbInflated = b.CbStored
				b.CRef = le16(e, 18)
			}
			x.bbtIndex[b.BREF.Bid] = len(x.BBTs)
			x.BBTs = append(x.BBTs, b)
			x.fillBlockType(len(x.BBTs) - 1)
		default:
			panic(fmt.Errorf("unexpected page entry type %#x", ptype))
		}
	}
}

func (x *XstFile) fillBlockType(ix int) {
	b := &x.BBTs[ix]
	b.Type = bTypeD
	if b.BREF.Bid%4 != 0 { // internal block: X/XX/SL/SI
		d := x.readBlock(b)
		if len(d) < 4 {
			return
		}
		b.Type = d[0]
		b.CLevel = int(d[1])
		b.CEnt = int(le16(d, 2))
		if b.Type == bTypeX && len(d) >= 8 {
			b.IcbTotal = le32(d, 4)
		}
	}
}

func isExternal(bid uint64) bool { return bid&0x02 == 0 }

func (x *XstFile) block(bid uint64) *srcBlock {
	ix, ok := x.bbtIndex[bid]
	if !ok {
		panic(fmt.Errorf("block %d not found in the BBT", bid))
	}
	return &x.BBTs[ix]
}

func (x *XstFile) hasBlock(bid uint64) bool {
	_, ok := x.bbtIndex[bid]
	return ok
}

// readBlock reads one block, inflating (OST) and decoding it as needed.
func (x *XstFile) readBlock(b *srcBlock) []byte {
	var d []byte
	if b.CbStored != b.CbInflated {
		raw := make([]byte, b.CbStored)
		x.readAt(raw, b.BREF.Ib)
		if len(raw) < 2 || raw[0] != 0x78 {
			panic(fmt.Errorf("OST DATA BLOCK ERROR: unexpected header in compressed data (bid %d)", b.BREF.Bid))
		}
		d = make([]byte, b.CbInflated)
		r := flate.NewReader(bytes.NewReader(raw[2:]))
		n, err := io.ReadFull(r, d)
		r.Close()
		if err != nil && n != len(d) {
			panic(fmt.Errorf("OST DATA BLOCK ERROR: inflate failed (bid %d): %v", b.BREF.Bid, err))
		}
	} else {
		d = make([]byte, b.CbStored)
		x.readAt(d, b.BREF.Ib)
	}
	if isExternal(b.BREF.Bid) && x.Header.CryptMethod == CryptPermute {
		cryptPermuteDecode(d)
	}
	return d
}

func (x *XstFile) readBlockBid(bid uint64) []byte { return x.readBlock(x.block(bid)) }

// readDatablocks returns the leaf data blocks of a data tree.
func (x *XstFile) readDatablocks(bid uint64) [][]byte {
	if isExternal(bid) {
		return [][]byte{x.readBlockBid(bid)}
	}
	var out [][]byte
	x.walkXBlock(bid, func(d []byte) { out = append(out, d) }, 0)
	return out
}

func (x *XstFile) walkXBlock(bid uint64, fn func([]byte), depth int) {
	if depth > 4 {
		panic(fmt.Errorf("data tree too deep (bid %d)", bid))
	}
	blk := x.readBlockBid(bid)
	if isExternal(bid) {
		fn(blk)
		return
	}
	cEnt := int(le16(blk, 2))
	for i := 0; i < cEnt; i++ {
		x.walkXBlock(x.cleanBid(le64(blk, 8+8*i)), fn, depth+1)
	}
}

// readFullDatablock returns the whole content of a block or data tree.
func (x *XstFile) readFullDatablock(bid uint64) []byte {
	if isExternal(bid) {
		return x.readBlockBid(bid)
	}
	var out []byte
	x.walkXBlock(bid, func(d []byte) { out = append(out, d...) }, 0)
	return out
}

// getSLEntries returns the subnode entries of a SL/SI tree.
func (x *XstFile) getSLEntries(bid uint64) []SLEntry {
	if bid == 0 {
		return nil
	}
	if isExternal(bid) {
		panic(fmt.Errorf("ENTRY ERROR (bid %d): subnode tree should be an internal block", bid))
	}
	var out []SLEntry
	x.collectSLEntries(bid, &out, 0)
	return out
}

func (x *XstFile) collectSLEntries(bid uint64, out *[]SLEntry, depth int) {
	if bid == 0 {
		return
	}
	if depth > 4 {
		panic(fmt.Errorf("subnode tree too deep (bid %d)", bid))
	}
	blk := x.readBlockBid(bid)
	if blk[0] != bTypeS {
		panic(fmt.Errorf("ENTRY ERROR (bid %d): not a SL tree", bid))
	}
	cLevel := blk[1]
	cEnt := int(le16(blk, 2))
	if cLevel == 0 {
		for i := 0; i < cEnt; i++ {
			o := 8 + 24*i
			e := SLEntry{
				Nid:     uint64(uint32(le64(blk, o))),
				BidData: x.cleanBid(le64(blk, o+8)),
				BidSub:  x.cleanBid(le64(blk, o+16)),
			}
			if e.Nid == 0 {
				continue
			}
			if !x.hasBlock(e.BidData) {
				x.warn(fmt.Sprintf("SL block (bid=%d) index=%d, nid=%d: bidData=%d not defined. Set to zero", bid, i, e.Nid, e.BidData))
				e.BidData = 0
			}
			if e.BidSub > 0 && !x.hasBlock(e.BidSub) {
				x.warn(fmt.Sprintf("SL block (bid=%d) index=%d, nid=%d: bidSub=%d not defined. Set to zero", bid, i, e.Nid, e.BidSub))
				e.BidSub = 0
			}
			*out = append(*out, e)
		}
		return
	}
	for i := 0; i < cEnt; i++ { // SI block
		x.collectSLEntries(x.cleanBid(le64(blk, 8+16*i+8)), out, depth+1)
	}
}

// Summary returns a short description of the file.
func (x *XstFile) Summary() string {
	return fmt.Sprintf("Filename   : %s\nFile Type  : %s\nFile Size  : %d kB\nFree area  : %d kB\nEncryption : %s\nNr of NBTs : %d\nNr of BBTs : %d",
		x.Filename, x.Type, x.Header.Root.FileEOF/1024, x.Header.Root.AMapFree/1024,
		cryptName(x.Header.CryptMethod), len(x.NBTs), len(x.BBTs))
}
