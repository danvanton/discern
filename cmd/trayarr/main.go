// Command trayarr is the Trayarr server: it watches optical drives, rips the
// right titles with MakeMKV and imports them into Radarr or a library folder.
//
// For now it only prints its version; the parts are wired in as they are
// built (see docs/DESIGN.md, section 18).
package main

import "fmt"

// version is set at build time with -ldflags "-X main.version=v0.1.0".
var version = "dev"

func main() {
	fmt.Println("trayarr", version)
}
