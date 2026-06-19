package main

import (
	"fmt"
	"log"
	"os"

	"github.com/mmwolf212/akita/internal/config"
)

func main(){
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}
	
	fmt.Fprintf(os.Stdout, "akita starting with %d watched domains\n", len(cfg.Watch.Domains))
}
