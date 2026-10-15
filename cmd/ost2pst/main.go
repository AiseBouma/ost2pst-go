// Command ost2pst converts Outlook OST files to PST and compacts PST files.
// Run without arguments for the graphical interface, or with -i for the
// command line. A file given without -i is opened in the GUI
// (on Windows you can drop an OST/PST onto the program):
//
//	ost2pst -i mailbox.ost -list
//	ost2pst -i mailbox.ost -folder "Top of Information Store" -o mailbox.pst
//	ost2pst -i archive.pst -o compacted.pst -reset-password
package main

import (
	"flag"
	"fmt"
	"os"
)

const version = "2.0.0-go (Oct-2026)"

func main() {
	in := flag.String("i", "", "input OST or PST file (command-line mode)")
	out := flag.String("o", "", "output PST file")
	folder := flag.String("folder", "", "OST folder to export: NID or path such as \"Top of Information Store/Inbox\" (default: mailbox top)")
	list := flag.Bool("list", false, "list the folders of the input file and exit")
	reset := flag.Bool("reset-password", false, "PST input: remove the store password")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("ost2pst", version)
		return
	}
	if *in != "" {
		os.Exit(runCLI(*in, *out, *folder, *list, *reset))
	}
	runGUI(flag.Arg(0))
}
