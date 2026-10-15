//go:build nogui

package main

import "fmt"

func runGUI(string) { fmt.Println("built without GUI; use -i to run on the command line (see -h)") }
