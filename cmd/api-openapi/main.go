package main

import (
	"encoding/json"
	"os"

	"github.com/varun6897/gpu/api"
)

// This small helper program allows `make openapi` to generate the OpenAPI spec
// by invoking the same builder used by the running API service.
func main() {
	doc := api.BuildOpenAPISpec()
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		os.Exit(1)
	}
}
