package main

import (
	"log"
	"os"

	"github.com/yusing/shadowtree/internal/shadowtreelsp"
)

func main() {
	log.SetFlags(0)
	if err := shadowtreelsp.Serve(os.Stdin, os.Stdout); err != nil {
		log.Fatal(err)
	}
}
