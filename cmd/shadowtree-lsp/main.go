package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/yusing/shadowtree/internal/shadowtreelsp"
)

func main() {
	log.SetFlags(0)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := shadowtreelsp.Serve(ctx, os.Stdin, os.Stdout); err != nil {
		log.Fatal(err)
	}
}
