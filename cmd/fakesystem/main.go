// fakesystem serves the fake openccu-lite the page's development and the Playwright tests run
// against (scripts/dev-serve.sh): the kit's fake of lite-rpc and the metadata, the warnings,
// the service messages, and the sinks that record what the channels send.
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/openccu-lite/lite-addon-notify/internal/fakesys"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18080", "the address to listen on")
	flag.Parse()
	s := fakesys.New()
	defer s.Close()
	s.SetWarnings(fakesys.Warning("backup-unencrypted", "plain", "warning", nil))
	log.Printf("fakesystem: on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, s.Handler()))
}
