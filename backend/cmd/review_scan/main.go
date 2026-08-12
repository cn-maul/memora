package main

import (
	"fmt"
	"os"

	"memora/internal/git"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: review_scan <repo-path>")
		os.Exit(2)
	}
	path := os.Args[1]

	tree, err := git.ScanForMemoraLeaks(path)
	if err != nil {
		fmt.Printf("[error] worktree/index scan: %v\n", err)
		os.Exit(1)
	}
	hist, err := git.ScanHistoryForMemoraLeaks(path)
	if err != nil {
		fmt.Printf("[error] history scan: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("== worktree/index leaks ==")
	if len(tree) == 0 {
		fmt.Println("  (none)")
	}
	for _, l := range tree {
		fmt.Println("  " + l)
	}
	fmt.Println("== history leaks ==")
	if len(hist) == 0 {
		fmt.Println("  (none)")
	}
	for _, l := range hist {
		fmt.Println("  " + l)
	}
}
