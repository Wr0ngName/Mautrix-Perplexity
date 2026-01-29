// mautrix-perplexity is a Matrix-Perplexity API puppeting bridge.
package main

import (
	"go.mau.fi/mautrix-perplexity/pkg/connector"
	"maunium.net/go/mautrix/bridgev2/matrix/mxmain"
)

// Information to find out exactly which commit the bridge was built from.
// These are filled at build time with the -X linker flag.
var (
	Tag       = "unknown"
	Commit    = "unknown"
	BuildTime = "unknown"
)

var m mxmain.BridgeMain

func main() {
	c := connector.NewConnector()
	m = mxmain.BridgeMain{
		Name:        "mautrix-perplexity",
		URL:         "https://github.com/mautrix/perplexity",
		Description: "A Matrix-Perplexity API bridge",
		Version:     "0.1.0",
		Connector:   c,
		PostInit:    postInit,
	}

	m.InitVersion(Tag, Commit, BuildTime)
	m.Run()
}

// postInit is called after bridge initialization but before start.
// We use this to set up the custom QueryHandler for ghost user existence queries.
func postInit() {
	// Set the QueryHandler on the appservice to handle ghost user queries
	m.Matrix.AS.QueryHandler = &connector.GhostQueryHandler{
		Matrix: m.Matrix,
		Log:    m.Log.With().Str("component", "query_handler").Logger(),
	}
}
