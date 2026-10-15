package pst

import "sort"

// exportTree writes leaf pages for the given entries and the branch levels
// above them, returning the BREF of the root page.
func (p *PstFile) exportTree(leaves [][]byte, keys []uint64, cbEnt int, ptype byte) BREF {
	maxLeaves := BTPageEntryBytes / cbEnt
	maxBranches := BTPageEntryBytes / BTcbEnt
	var branch []BTEntry
	for start := 0; start < len(leaves); start += maxLeaves {
		end := start + maxLeaves
		if end > len(leaves) {
			end = len(leaves)
		}
		ref := p.writeBTPage(leaves[start:end], cbEnt, ptype, 0)
		branch = append(branch, BTEntry{Key: keys[start], BREF: ref})
	}
	if len(branch) == 0 { // empty tree: a single empty leaf page
		return p.writeBTPage(nil, cbEnt, ptype, 0)
	}
	for level := 1; len(branch) > 1; level++ {
		var upper []BTEntry
		for start := 0; start < len(branch); start += maxBranches {
			end := start + maxBranches
			if end > len(branch) {
				end = len(branch)
			}
			entries := make([][]byte, 0, end-start)
			for _, e := range branch[start:end] {
				b := make([]byte, BTcbEnt)
				e.marshal(b)
				entries = append(entries, b)
			}
			ref := p.writeBTPage(entries, BTcbEnt, ptype, level)
			upper = append(upper, BTEntry{Key: branch[start].Key, BREF: ref})
		}
		branch = upper
	}
	return branch[0].BREF
}

// ExportNBT sorts and writes the node B-tree.
func (p *PstFile) ExportNBT() {
	sort.SliceStable(p.NBTs, func(i, j int) bool { return p.NBTs[i].Nid < p.NBTs[j].Nid })
	leaves := make([][]byte, len(p.NBTs))
	keys := make([]uint64, len(p.NBTs))
	for i, e := range p.NBTs {
		b := make([]byte, NBTcbEnt)
		e.marshal(b)
		leaves[i] = b
		keys[i] = uint64(e.Nid)
	}
	p.Header.Root.NBT = p.exportTree(leaves, keys, NBTcbEnt, ptypeNBT)
}

// ExportBBT sorts and writes the block B-tree.
func (p *PstFile) ExportBBT() {
	sort.SliceStable(p.BBTs, func(i, j int) bool { return p.BBTs[i].Bid < p.BBTs[j].Bid })
	for i, e := range p.BBTs {
		p.bbtIndex[e.Bid] = i
	}
	leaves := make([][]byte, len(p.BBTs))
	keys := make([]uint64, len(p.BBTs))
	for i, e := range p.BBTs {
		b := make([]byte, BBTcbEnt)
		e.marshal(b)
		leaves[i] = b
		keys[i] = e.Bid
	}
	p.Header.Root.BBT = p.exportTree(leaves, keys, BBTcbEnt, ptypeBBT)
}
