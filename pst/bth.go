package pst

import (
	"fmt"
	"sort"
)

// Node kinds of the in-memory BTH tree / heap.
const (
	kindHData = iota
	kindLeaf
	kindBranch
)

type node struct {
	kind  int
	level int
	key   uint32
	hnIx  int // allocation order on the heap (1 based)
	hid   HID
	data  []byte // heap data (kindHData)

	// leaf
	isPC   bool
	pcID   PropID
	pcType PropType
	pcHNID HNID
	prop   *Property
	row    tcRowID

	// branch
	children []*node
	bth      *bth
}

func (n *node) size() int {
	switch n.kind {
	case kindHData:
		return len(n.data)
	case kindLeaf:
		return 8
	}
	if n.level == 0 {
		return len(n.children) * (n.bth.cbKey + n.bth.cbEnt)
	}
	return len(n.children) * (n.bth.cbKey + 4)
}

func (n *node) bytes() []byte {
	switch n.kind {
	case kindHData:
		return n.data
	case kindLeaf:
		d := make([]byte, 8)
		if n.isPC {
			put16(d, 0, uint16(n.pcID))
			put16(d, 2, uint16(n.pcType))
			switch {
			case n.hid.typ() == NidHID:
				put32(d, 4, uint32(n.hid))
			case !n.prop.hasFixedSize() && len(n.prop.Data) == 0:
				put32(d, 4, 0)
			default:
				put32(d, 4, uint32(n.pcHNID))
			}
		} else {
			put32(d, 0, n.row.rowID)
			put32(d, 4, n.row.rowIndex)
		}
		return d
	}
	d := make([]byte, n.size())
	if n.level == 0 {
		for i, c := range n.children {
			copy(d[i*8:], c.bytes())
		}
		return d
	}
	for i, c := range n.children {
		if n.bth.cbKey == 2 {
			put16(d, i*6, uint16(c.key))
			put32(d, i*6+2, uint32(c.hid))
		} else {
			put32(d, i*8, c.key)
			put32(d, i*8+4, uint32(c.hid))
		}
	}
	return d
}

func (n *node) isFull() bool {
	if n.level == 0 {
		return len(n.children) >= n.bth.maxLeaves
	}
	return len(n.children) >= n.bth.maxBranches
}

func (n *node) addLeaf(l *node) bool {
	if n.level == 0 {
		if !n.isFull() {
			if len(n.children) == 0 {
				n.key = l.key // branch key = its first leaf key
			}
			n.children = append(n.children, l)
			return true
		}
		return false
	}
	for _, c := range n.children {
		if c.kind == kindBranch && c.addLeaf(l) {
			return true
		}
	}
	if !n.isFull() {
		nb := newBranch(n.bth, l.key, n.level-1)
		nb.addLeaf(l)
		n.children = append(n.children, nb)
		return true
	}
	return false
}

func newBranch(b *bth, key uint32, level int) *node {
	return &node{kind: kindBranch, bth: b, key: key, level: level}
}

func newHData(data []byte) *node {
	return &node{kind: kindHData, level: -1, data: data}
}

func newPCLeaf(p *Property) *node {
	l := &node{kind: kindLeaf, level: -1, isPC: true, key: uint32(uint16(p.ID)),
		pcID: p.ID, pcType: p.Type, pcHNID: p.HNID, prop: p}
	switch {
	case p.hasFixedSize() && p.dataSize() <= 4:
		l.hid = makeHID(NidInternal, 0, 0) // value is in the HNID
	case p.dataSize() > MaxHeapItemSize || p.HNID.isNID():
		l.hid = HID(p.HNID) // keep the subnode nid
	case len(p.Data) != 0:
		l.hid = makeHID(NidHID, 0, 0) // heap item, hid computed later
	default:
		l.hid = makeHID(NidInternal, 0, 0)
	}
	return l
}

func newRowLeaf(r tcRowID) *node {
	return &node{kind: kindLeaf, level: -1, key: r.rowID, row: r}
}

type heapItem struct {
	hid  HID
	data []byte
}

type rowCellRef struct {
	rowIndex int
	rowPos   int
	nodeIx   int
}

// bth builds a BTH-on-heap for a PC or a TC and lays it out on heap blocks.
type bth struct {
	w           *blockWriter
	cbKey       int
	cbEnt       int
	bIdxLevels  byte
	hidRoot     HID
	maxLeaves   int
	maxBranches int
	nodes       []*node
	hnHIDs      []HID
	hidBlockIx  uint16
	subnodeNID  uint32
	rowMatrixSN uint32
	newSubnodes bool
	subnodes    []SLEntry
	root        *node
	tc          *TableContext
	props       []Property
	heapItems   []heapItem
	hidCells    []rowCellRef
}

func (b *bth) headerBytes() []byte {
	d := make([]byte, 8)
	d[0] = bTypeBTH
	d[1] = byte(b.cbKey)
	d[2] = byte(b.cbEnt)
	d[3] = b.bIdxLevels
	put32(d, 4, uint32(b.hidRoot))
	return d
}

func (b *bth) headerNode() *node {
	n := newHData(b.headerBytes())
	n.hnIx = 1
	n.hid = makeHID(NidHID, 1, 0)
	return n
}

func (b *bth) tcHeaderNode() *node {
	n := newHData(b.tc.header())
	n.hnIx = 2
	n.hid = makeHID(NidHID, 2, 0)
	return n
}

// newPCBTH builds the BTH of a Property Context. props must be sorted by id.
func newPCBTH(w *blockWriter, props []Property) *bth {
	b := &bth{w: w, props: props, cbKey: 2, cbEnt: 6, hidRoot: makeHID(NidHID, 2, 0)}
	b.root = newBranch(b, 0, 0)
	b.maxLeaves = MaxHeapItemSize / (b.cbKey + b.cbEnt)
	b.maxBranches = MaxHeapItemSize / (b.cbKey + 4)
	for i := range b.props {
		b.insertLeaf(newPCLeaf(&b.props[i]))
	}
	b.bIdxLevels = byte(b.root.level)
	b.nodes = []*node{b.headerNode()}
	b.addTreeNodes()
	b.addPCDataItems(b.root)
	b.updateHIDs()
	b.updatePCLeafHIDs(b.root)
	b.finish()
	return b
}

// newTCBTH builds the BTH of a Table Context. With withSubnodes the row matrix
// and large cells are written to new subnodes.
func newTCBTH(w *blockWriter, tc *TableContext, withSubnodes bool) *bth {
	b := &bth{w: w, tc: tc, newSubnodes: withSubnodes, cbKey: 4, cbEnt: 4}
	if withSubnodes {
		b.subnodeNID = 1
		if tc.info.hnidRows.isNID() {
			b.rowMatrixSN = uint32(tc.info.hnidRows)
			b.subnodeNID = b.rowMatrixSN >> 5
		}
	}
	b.root = newBranch(b, 0, 0)
	if len(tc.rowIndexes) == 0 {
		b.nodes = []*node{b.headerNode(), b.tcHeaderNode()}
	} else {
		b.maxLeaves = MaxHeapItemSize / (b.cbKey + b.cbEnt)
		b.maxBranches = MaxHeapItemSize / (b.cbKey + 4)
		b.hidRoot = makeHID(NidHID, 3, 0) // row index root is the 3rd heap item
		for _, r := range tc.rowIndexes {
			b.insertLeaf(newRowLeaf(r))
		}
		b.bIdxLevels = byte(b.root.level)
		b.buildTCHeap()
	}
	b.finish()
	return b
}

func (b *bth) insertLeaf(l *node) {
	if !b.root.addLeaf(l) { // root full: add a level
		nr := newBranch(b, b.root.key, b.root.level+1)
		nr.children = append(nr.children, b.root)
		nr.addLeaf(l)
		b.root = nr
	}
}

func (b *bth) finish() {
	b.heapItems = make([]heapItem, len(b.nodes))
	for i, n := range b.nodes {
		b.heapItems[i] = heapItem{hid: n.hid, data: n.bytes()}
	}
	sort.SliceStable(b.subnodes, func(i, j int) bool { return b.subnodes[i].Nid < b.subnodes[j].Nid })
}

func (b *bth) buildTCHeap() {
	tc := b.tc
	b.nodes = []*node{b.headerNode(), b.tcHeaderNode()}
	b.addTreeNodes()
	b.addTCDataItems()
	inHeap := tc.rowMatrixSize() <= MaxHeapItemSize
	if inHeap { // placeholder used to compute the hids
		b.addNode(newHData(make([]byte, tc.rowMatrixSize())))
	}
	b.updateHIDs()
	for _, c := range b.hidCells {
		put32(tc.rows[c.rowIndex].data, c.rowPos, uint32(b.hnHIDs[c.nodeIx]))
	}
	if inHeap {
		last := b.nodes[len(b.nodes)-1]
		last.data = tc.rowMatrixBlocks()[0]
		tc.info.hnidRows = HNID(last.hid)
	} else if b.newSubnodes {
		if b.rowMatrixSN == 0 || HNID(b.rowMatrixSN).isHID() {
			// the source kept its rows on the heap: give the matrix its own subnode
			b.subnodeNID++
			b.rowMatrixSN = makeNID(NidLTP, b.subnodeNID)
			tc.info.hnidRows = HNID(b.rowMatrixSN)
		}
		b.subnodes = append(b.subnodes, b.addSubnodeBlocks(b.rowMatrixSN, tc.rowMatrixBlocks()))
	}
	b.nodes[1].data = b.tc.header() // overwrite with the updated header
}

func (b *bth) addTreeNodes() {
	b.addNode(b.root)
	for level := b.root.level - 1; level >= 0; level-- {
		b.addNodesOnLevel(level, b.root)
	}
}

func (b *bth) addNode(n *node) {
	n.hnIx = len(b.nodes) + 1
	b.nodes = append(b.nodes, n)
}

func (b *bth) addNodesOnLevel(level int, br *node) {
	for _, c := range br.children {
		if c.level == level {
			b.addNode(c)
		} else if c.kind == kindBranch {
			b.addNodesOnLevel(level, c)
		}
	}
}

func (b *bth) addPCDataItems(br *node) {
	for _, c := range br.children {
		if br.level != 0 {
			b.addPCDataItems(c)
			continue
		}
		if c.pcHNID.hasValue() && c.hid.typ() == NidHID {
			h := newHData(c.prop.Data)
			h.hid = c.hid
			b.addNode(h)
			c.hnIx = len(b.nodes)
		}
	}
}

func (b *bth) updatePCLeafHIDs(br *node) {
	for _, c := range br.children {
		if br.level != 0 {
			b.updatePCLeafHIDs(c)
			continue
		}
		if c.hid.typ() == NidHID {
			c.hid = b.hnHIDs[c.hnIx]
		}
	}
}

func (b *bth) addTCDataItems() {
	tc := b.tc
	for i, rd := range tc.rows {
		for j := range rd.props {
			p := &rd.props[j]
			col := tc.findColumn(p.ID, p.Type)
			if col < 0 {
				continue
			}
			cb := int(tc.cols[col].cbData)
			ib := int(tc.cols[col].ibData)
			bit := int(tc.cols[col].iBit)
			if p.hasFixedSize() && p.dataSize() <= 8 { // fits in the row
				setBit(rd.ceb, bit)
				if p.ID != PidTagLtpRowVer { // the PST gets new row versions
					copy(rd.data[ib:], p.Data)
				}
				continue
			}
			if p.HNID.hasValue() || p.dataSize() > 0 {
				setBit(rd.ceb, bit)
				if cb != 4 {
					panic(fmt.Errorf("property id %#x cb=%d expected 4", p.ID, cb))
				}
				switch {
				case p.dataSize() > MaxHeapItemSize: // value goes to a subnode
					if b.newSubnodes {
						b.subnodeNID++
						sn := makeNID(NidLTP, b.subnodeNID)
						put32(rd.data, ib, sn)
						b.subnodes = append(b.subnodes, b.addSubnode(sn, p.Data))
					} else { // keep the original nid
						put32(rd.data, ib, uint32(p.HNID))
					}
				case p.dataSize() > 0: // value goes on the heap
					b.addNode(newHData(p.Data))
					b.hidCells = append(b.hidCells, rowCellRef{rowIndex: i, rowPos: ib, nodeIx: len(b.nodes)})
				}
			} else if p.ID == PidTagMessageClassW {
				// SCANPST flags a missing default message class when the CEB bit is clear
				setBit(rd.ceb, bit)
			}
		}
	}
}

// updateHIDs assigns HIDs (block, index) to every heap node in order.
func (b *bth) updateHIDs() {
	b.hnHIDs = make([]HID, len(b.nodes)+1) // HIDs are 1 based
	b.hidBlockIx = 0
	hidIx := uint16(0)
	used := hnDatablockUsage(0)
	content := hnHeaderSize(0) // bytes before the page map, to keep it 2-byte aligned
	for _, n := range b.nodes {
		sz := n.size()
		if used+sz+2+(content+sz)%2 > MaxDatablockSize {
			hidIx = 0
			b.hidBlockIx++
			used = hnDatablockUsage(int(b.hidBlockIx))
			content = hnHeaderSize(int(b.hidBlockIx))
		}
		used += sz + 2
		content += sz
		hidIx++
		b.hnHIDs[n.hnIx] = makeHID(NidHID, hidIx, b.hidBlockIx)
	}
	for _, n := range b.nodes {
		n.hid = b.hnHIDs[n.hnIx]
	}
}

func hnHeaderSize(ix int) int {
	if ix == 0 {
		return 12
	}
	if (ix-8)%128 == 0 {
		return 66
	}
	return 2
}

func (b *bth) addSubnode(nid uint32, data []byte) SLEntry {
	r := b.w.addDatablock(data, 0)
	return SLEntry{Nid: uint64(nid), BidData: r.Bid}
}

func (b *bth) addSubnodeBlocks(nid uint32, data [][]byte) SLEntry {
	if len(data) == 1 {
		return b.addSubnode(nid, data[0])
	}
	r := b.w.addDataTree(data)
	return SLEntry{Nid: uint64(nid), BidData: r.Bid}
}

// heapBlocks lays the heap items out on HN data blocks ([MS-PST] 2.3.1).
func (b *bth) heapBlocks(clientSig byte) [][]byte {
	var out [][]byte
	for i := 0; i <= int(b.hidBlockIx); i++ {
		out = append(out, b.hnDatablock(i, clientSig))
	}
	return out
}

func (b *bth) hnDatablock(blockIx int, clientSig byte) []byte {
	hn := newHNDatablock(blockIx, int(b.hidBlockIx), clientSig)
	rgibAlloc := []uint16{uint16(len(hn))}
	for _, h := range b.heapItems {
		if int(h.hid.block(false)) != blockIx {
			continue
		}
		hn = append(hn, h.data...)
		rgibAlloc = append(rgibAlloc, uint16(len(hn)))
	}
	return closeHNDatablock(hn, rgibAlloc, blockIx == int(b.hidBlockIx))
}

func newHNDatablock(blockIx, lastIx int, clientSig byte) []byte {
	nrBlocks := lastIx - blockIx + 1
	if blockIx == 0 { // HNHDR
		hn := make([]byte, 12)
		hn[2] = 0xEC
		hn[3] = clientSig
		userRoot := makeHID(NidHID, 1, 0)
		if clientSig == bTypeTC {
			userRoot = makeHID(NidHID, 2, 0)
		}
		put32(hn, 4, uint32(userRoot))
		if nrBlocks > 8 {
			nrBlocks = 8
		}
		fillLevel(hn, 8, nrBlocks)
		return hn
	}
	if (blockIx-8)%128 == 0 { // HNBITMAPHDR
		hn := make([]byte, 66)
		if nrBlocks > 128 {
			nrBlocks = 128
		}
		fillLevel(hn, 2, nrBlocks)
		return hn
	}
	return make([]byte, 2) // HNPAGEHDR
}

// fillLevel marks the blocks as fully allocated.
func fillLevel(hn []byte, ix, nrBlocks int) {
	n := nrBlocks / 2
	for i := 0; i < n; i++ {
		hn[ix+i] = 0xff
	}
	if nrBlocks%2 != 0 {
		hn[ix+n] = 0x0f
	}
}

func closeHNDatablock(hn []byte, rgibAlloc []uint16, last bool) []byte {
	size := len(hn)
	if size%2 != 0 {
		size++ // HNPAGEMAP is 2-byte aligned
	}
	ibHnpm := size
	size += len(rgibAlloc)*2 + 4
	if !last {
		if size > MaxDatablockSize {
			panic(fmt.Errorf("heap block overflow (%d bytes)", size))
		}
		size = MaxDatablockSize // blocks inside a data tree have a fixed size
	}
	out := make([]byte, size)
	copy(out, hn)
	put16(out, 0, uint16(ibHnpm))
	put16(out, ibHnpm, uint16(len(rgibAlloc)-1)) // cAlloc
	put16(out, ibHnpm+2, 0)                      // cFree
	for i, v := range rgibAlloc {
		put16(out, ibHnpm+4+i*2, v)
	}
	return out
}
