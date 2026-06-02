// Command yotta-models refreshes internal/catalog/catalog.gen.json by
// querying each cloud provider's list-models endpoint.
//
// Usage:
//
//	go run ./cmd/yotta-models refresh
//	go run ./cmd/yotta-models refresh --output internal/catalog/catalog.gen.json
//
// Required env vars (a missing key skips that provider with a warning):
//
//	ANTHROPIC_API_KEY
//	OPENAI_API_KEY
//	GEMINI_API_KEY
//
// This is a maintainer tool, not user-facing. Run it when a provider
// announces new models; commit the diff. CI may eventually run it on
// a schedule and open a PR — not yet.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/yottadynamics/yottacode/internal/catalog"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "refresh":
		fs := flag.NewFlagSet("refresh", flag.ExitOnError)
		out := fs.String("output", "internal/catalog/catalog.gen.json",
			"path to the catalog file to rewrite")
		_ = fs.Parse(os.Args[2:])
		if err := refresh(*out); err != nil {
			fmt.Fprintf(os.Stderr, "refresh failed: %v\n", err)
			os.Exit(1)
		}
	case "refresh-modelsdev":
		fs := flag.NewFlagSet("refresh-modelsdev", flag.ExitOnError)
		out := fs.String("output", "internal/catalog/models-dev.gen.json",
			"path to the embedded models.dev snapshot to rewrite")
		_ = fs.Parse(os.Args[2:])
		n, err := catalog.WriteModelsDevSnapshot(*out)
		if err != nil {
			fmt.Fprintf(os.Stderr, "refresh-modelsdev failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "wrote %s (%d providers from models.dev)\n", *out, n)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `yotta-models — refresh the embedded model catalog

Usage:
  yotta-models refresh [--output <path>]            refresh catalog.gen.json (curated providers)
  yotta-models refresh-modelsdev [--output <path>]  refresh the embedded models.dev snapshot

Required env vars for refresh (one or more):
  ANTHROPIC_API_KEY    Anthropic Console key
  OPENAI_API_KEY       OpenAI platform key
  GEMINI_API_KEY       Google AI Studio key

A missing key skips that provider with a warning; existing entries
for that provider in the output file are preserved untouched.
refresh-modelsdev needs no keys — it only downloads the public
models.dev catalog and writes the offline-first embedded snapshot.
`)
}
