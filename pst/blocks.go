package pst

import "fmt"

// blockWriter writes data blocks and subnode trees to the output PST, copying
// from the source file where needed. Blocks built from a source bid are
// remembered so that shared blocks are written once and ref-counted.
type blockWriter struct {
	out    *PstFile
	src    *XstFile
	bidMap map[uint64]BREF
}

func newBlockWriter(out *PstFile, src *XstFile) *blockWriter {
	return &blockWriter{out: out, src: src, bidMap: map[uint64]BREF{}}
}

func (w *blockWriter) mapped(srcBid uint64) (BREF, bool) {
	if srcBid == 0 {
		return BREF{}, false
	}
	r, ok := w.bidMap[srcBid]
	return r, ok
}

func (w *blockWriter) addMapping(srcBid uint64, r BREF) {
	if srcBid == 0 {
		return
	}
	if _, ok := w.bidMap[srcBid]; ok {
		panic(fmt.Errorf("ERROR MAPPING BID: source bid %d already mapped", srcBid))
	}
	w.bidMap[srcBid] = r
}

// addDatablock writes one block (or a data tree when it is too large).
func (w *blockWriter) addDatablock(data []byte, srcBid uint64) BREF {
	if r, ok := w.mapped(srcBid); ok {
		w.out.incrementRefCount(r.Bid)
		return r
	}
	var r BREF
	if len(data) <= MaxDatablockSize {
		r = w.out.newDataBlock(len(data), false)
		w.out.writeBlock(r, data)
	} else { // uncompressed OST block larger than 8176: make a new X tree
		r = w.addDataTree(splitBlocks(data))
	}
	w.addMapping(srcBid, r)
	return r
}

func (w *blockWriter) addDatablocks(blocks [][]byte, srcBid uint64) BREF {
	if r, ok := w.mapped(srcBid); ok {
		w.out.incrementRefCount(r.Bid)
		return r
	}
	if len(blocks) == 1 {
		return w.addDatablock(blocks[0], srcBid)
	}
	return w.addDataTree(blocks)
}

func splitBlocks(data []byte) [][]byte {
	var out [][]byte
	for len(data) > MaxDatablockSize {
		out = append(out, data[:MaxDatablockSize])
		data = data[MaxDatablockSize:]
	}
	return append(out, data)
}

type treeBlock struct {
	ref BREF
	cb  uint32
}

// addDataTree writes the blocks under an XBLOCK (or XXBLOCK) tree.
func (w *blockWriter) addDataTree(blocks [][]byte) BREF {
	n := len(blocks)
	xx := n > MaxXblockEntries
	var xxBlock treeBlock
	if xx {
		nx := (n + MaxXblockEntries - 1) / MaxXblockEntries
		cb := 8 + nx*8
		xxBlock = treeBlock{ref: w.out.newDataBlock(cb, true), cb: uint32(cb)}
	}
	var xBlocks, leaves []treeBlock
	total := uint32(0)
	for i, b := range blocks {
		if len(b) > MaxBlockSize {
			panic(fmt.Errorf("block too large"))
		}
		total += uint32(len(b))
		if i%MaxXblockEntries == 0 {
			entries := n - i
			if entries > MaxXblockEntries {
				entries = MaxXblockEntries
			}
			cb := 8 + entries*8
			xBlocks = append(xBlocks, treeBlock{ref: w.out.newDataBlock(cb, true), cb: uint32(cb)})
		}
		leaves = append(leaves, treeBlock{ref: w.addDatablock(b, 0), cb: uint32(len(b))})
	}
	for i, xb := range xBlocks {
		d := make([]byte, xb.cb)
		d[0] = bTypeX
		d[1] = 1
		cEnt := int(xb.cb-8) / 8
		put16(d, 2, uint16(cEnt))
		sum := uint32(0)
		for j := 0; j < cEnt; j++ {
			l := leaves[i*MaxXblockEntries+j]
			sum += l.cb
			put64(d, 8+j*8, l.ref.Bid)
		}
		put32(d, 4, sum)
		w.out.writeBlock(xb.ref, d)
	}
	if !xx {
		return xBlocks[0].ref
	}
	d := make([]byte, xxBlock.cb)
	d[0] = bTypeX
	d[1] = 2
	put16(d, 2, uint16(len(xBlocks)))
	put32(d, 4, total)
	for i, xb := range xBlocks {
		put64(d, 8+i*8, xb.ref.Bid)
	}
	w.out.writeBlock(xxBlock.ref, d)
	return xxBlock.ref
}

// addSLEntries writes a subnode tree (SLBLOCK, or SIBLOCK above SLBLOCKs).
func (w *blockWriter) addSLEntries(entries []SLEntry) BREF {
	n := len(entries)
	if n == 0 {
		return BREF{}
	}
	if n > MaxSLblockEntries {
		return w.addSIEntries(entries)
	}
	cb := n*24 + 8
	ref := w.out.newDataBlock(cb, true)
	d := make([]byte, cb)
	d[0] = bTypeS
	d[1] = 0
	put16(d, 2, uint16(n))
	for i, e := range entries {
		put64(d, 8+i*24, e.Nid)
		put64(d, 16+i*24, e.BidData)
		put64(d, 24+i*24, e.BidSub)
	}
	w.out.writeBlock(ref, d)
	return ref
}

func (w *blockWriter) addSIEntries(entries []SLEntry) BREF {
	nSI := (len(entries) + MaxSLblockEntries - 1) / MaxSLblockEntries
	cb := nSI*16 + 8
	if cb > MaxDatablockSize {
		panic(fmt.Errorf("SL/SI subnode overflow: cannot support %d entries", len(entries)))
	}
	ref := w.out.newDataBlock(cb, true)
	d := make([]byte, cb)
	d[0] = bTypeS
	d[1] = 1
	put16(d, 2, uint16(nSI))
	for i := 0; i < nSI; i++ {
		start := i * MaxSLblockEntries
		end := start + MaxSLblockEntries
		if end > len(entries) {
			end = len(entries)
		}
		sl := w.addSLEntries(entries[start:end])
		put64(d, 8+i*16, entries[start].Nid)
		put64(d, 16+i*16, sl.Bid)
	}
	w.out.writeBlock(ref, d)
	return ref
}

// copySLEntry copies a subnode (data and nested subnodes) from the source.
func (w *blockWriter) copySLEntry(sl SLEntry) SLEntry {
	n := SLEntry{Nid: sl.Nid}
	if r, ok := w.mapped(sl.BidData); ok {
		w.out.incrementRefCount(r.Bid)
		n.BidData = r.Bid
	} else {
		n.BidData = w.addDatablock(w.src.readFullDatablock(sl.BidData), sl.BidData).Bid
	}
	if sl.BidSub > 0 {
		if r, ok := w.mapped(sl.BidSub); ok {
			w.out.incrementRefCount(r.Bid)
			n.BidSub = r.Bid
		} else {
			n.BidSub = w.copySubnodes(w.src.getSLEntries(sl.BidSub), nil).Bid
		}
	}
	return n
}

func (w *blockWriter) copySubnode(bidSub uint64, replace *SLEntry) BREF {
	return w.copySubnodes(w.src.getSLEntries(bidSub), replace)
}

// copySubnodes copies a list of subnodes; the entry with replace's nid is
// replaced by replace instead of being copied.
func (w *blockWriter) copySubnodes(src []SLEntry, replace *SLEntry) BREF {
	dest := make([]SLEntry, 0, len(src))
	for _, s := range src {
		if replace != nil && s.Nid == replace.Nid {
			dest = append(dest, *replace)
		} else {
			dest = append(dest, w.copySLEntry(s))
		}
		w.out.updateSubnodeNIDHighWaterMark(uint32(s.Nid))
	}
	return w.addSLEntries(dest)
}

func (w *blockWriter) copyDatablock(srcBid uint64) BREF {
	return w.addDatablock(w.src.readFullDatablock(srcBid), srcBid)
}
