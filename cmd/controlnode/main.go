package main

import (
	"dfsha/internal/common"
	"encoding/json"
	"log"
	"os"
)

func main() {
	cfg, e := loadConfig()
	if e != nil {
		log.Fatal(e)
	}
	s := newServer(cfg)
	b, e := os.ReadFile(cfg.UsersFile)
	if e != nil {
		log.Fatal(e)
	}
	if e = json.Unmarshal(b, &s.users); e != nil || len(s.users) == 0 {
		log.Fatal("users.json inválido o vacío")
	}
	if e = s.startRaft(); e != nil {
		log.Fatal(e)
	}
	go s.maintenanceLoop()
	log.Printf("ControlNode %s en %s", cfg.NodeID, cfg.Addr)
	log.Fatal(common.Serve(cfg.Addr, s.routes(), cfg.Security))
}
