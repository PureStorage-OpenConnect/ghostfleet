// vcsim starts a local vCenter API simulator for development and E2E tests:
//
//	go run ./tools/vcsim
//
// It serves a VPX inventory (datacenter DC0, cluster DC0_C0, datastore
// LocalDS_0, network "VM Network") on https://127.0.0.1:8989/sdk with
// credentials user/pass — point a GhostFleet connection at it with
// "skip TLS verification" enabled.
package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"net/url"
	"os"

	"github.com/vmware/govmomi/simulator"
	_ "github.com/vmware/govmomi/vapi/simulator" // registers vAPI endpoints (tags)
)

func main() {
	listen := flag.String("l", "127.0.0.1:8989", "listen address")
	flag.Parse()

	model := simulator.VPX()
	defer model.Remove()
	if err := model.Create(); err != nil {
		fmt.Fprintln(os.Stderr, "creating inventory:", err)
		os.Exit(1)
	}
	model.Service.Listen = &url.URL{
		Host: *listen,
		User: url.UserPassword("user", "pass"),
	}
	model.Service.TLS = new(tls.Config)
	model.Service.RegisterEndpoints = true

	server := model.Service.NewServer()
	defer server.Close()
	fmt.Printf("vcsim running at %s (user/pass)\n", server.URL)
	select {}
}
