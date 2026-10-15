package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/AiseBouma/ost2pst-go/pst"
)

// consoleReporter prints progress to the terminal.
type consoleReporter struct {
	live bool // the last printed line is a live (replaceable) line
}

func (r *consoleReporter) Status(msg string) {
	if r.live {
		fmt.Println()
		r.live = false
	}
	if msg != "" {
		fmt.Println(msg)
	}
}

func (r *consoleReporter) StatusReplace(msg string) {
	fmt.Printf("\r%-70s", msg)
	r.live = true
}

func (r *consoleReporter) Progress(current, total int, phase string) {}

func printTree(f *pst.Folder, depth int) {
	name := f.Name
	if f.IsRoot() {
		name = "(root)"
	}
	fmt.Printf("%8d  %s%s\n", f.Nid, strings.Repeat("  ", depth), name)
	for _, ch := range f.Children {
		printTree(ch, depth+1)
	}
}

// findFolder finds a folder by NID or by its path (e.g. "Top of Information Store/Inbox").
func findFolder(c *pst.Converter, sel string) *pst.Folder {
	if n, err := strconv.ParseUint(sel, 10, 32); err == nil {
		for _, f := range c.Folders {
			if f.Nid == uint32(n) {
				return f
			}
		}
		return nil
	}
	parts := strings.Split(strings.Trim(sel, "/"), "/")
	cur := c.Root
	for _, p := range parts {
		var next *pst.Folder
		for _, ch := range cur.Children {
			if strings.EqualFold(ch.Name, p) {
				next = ch
				break
			}
		}
		if next == nil {
			return nil
		}
		cur = next
	}
	return cur
}

// defaultFolder returns the mailbox top folder.
func defaultFolder(c *pst.Converter) *pst.Folder {
	if f := c.MailboxTop(); f != nil {
		return f
	}
	if len(c.Root.Children) > 0 {
		return c.Root.Children[0]
	}
	return nil
}

func runCLI(src, dst, folder string, list, resetPassword bool) int {
	rep := &consoleReporter{}
	c := pst.NewConverter(rep)
	if err := c.Open(src); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	defer c.Close()
	fmt.Println(c.Source().Summary())
	if list {
		fmt.Println("\n     NID  folder")
		printTree(c.Root, 0)
		return 0
	}
	if dst == "" {
		fmt.Fprintln(os.Stderr, "error: an output file is required (-o file.pst)")
		return 2
	}
	var nid uint32
	if c.Source().Type == pst.FileOST {
		var f *pst.Folder
		if folder == "" {
			f = defaultFolder(c)
		} else {
			f = findFolder(c, folder)
		}
		if f == nil {
			fmt.Fprintln(os.Stderr, "error: folder not found; use -list to see the folders")
			return 2
		}
		fmt.Printf("Exporting folder %q (NID %d)\n", f.Name, f.Nid)
		nid = f.Nid
	}
	if err := c.Export(dst, nid, resetPassword); err != nil {
		rep.Status("")
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Println()
	fmt.Println(c.LastOutput)
	return 0
}
