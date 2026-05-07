package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/serpro69/capy/internal/store"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: dump-session <db-path> <source-label-substring>\n")
		os.Exit(1)
	}
	dbPath := os.Args[1]
	needle := os.Args[2]

	s := store.NewContentStore(dbPath, "", 0)
	defer s.Close()

	sources, err := s.ListSources()
	if err != nil {
		fmt.Fprintf(os.Stderr, "listing sources: %v\n", err)
		os.Exit(1)
	}

	var matched *store.SourceInfo
	for _, src := range sources {
		if strings.Contains(src.Label, needle) && src.Kind == store.KindSession {
			matched = &src
			break
		}
	}
	if matched == nil {
		fmt.Fprintf(os.Stderr, "no session source matching %q\n", needle)
		os.Exit(1)
	}

	chunks, err := s.GetChunksBySource(matched.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "getting chunks: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("# Raw v1 Session Dump\n\n")
	fmt.Printf("> Source: `%s`\n", matched.Label)
	fmt.Printf("> Kind: %s\n", matched.Kind)
	fmt.Printf("> Chunks: %d\n", matched.ChunkCount)
	fmt.Printf("> Indexed at: %s\n\n", matched.IndexedAt.Format("2006-01-02T15:04:05Z"))
	fmt.Printf("---\n\n")

	for i, c := range chunks {
		fmt.Printf("## Chunk %d: %s\n\n", i+1, c.Title)
		fmt.Printf("````\n%s\n````\n\n", c.Content)
		fmt.Printf("---\n\n")
	}
}
