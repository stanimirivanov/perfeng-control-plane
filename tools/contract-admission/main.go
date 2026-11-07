// Command contract-admission checks candidate Run/native bytes with the actual
// provider loader without selecting them for runtime use.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"testing/fstest"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
)

func main() {
	directory := flag.String("snapshot", "internal/contract/snapshot", "candidate snapshot directory")
	flag.Parse()
	if err := admit(*directory, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func admit(directory string, output io.Writer) error {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	// Freeze the complete inputs before validation and receipt hashing. No
	// subsequent candidate filesystem read can change what the receipt describes.
	candidate := fstest.MapFS{}
	hashes := map[string]string{}
	for _, path := range []string{"openapi.json", "api/native-worker/v2/openapi.json"} {
		data, readErr := fs.ReadFile(root.FS(), path)
		if readErr != nil {
			return readErr
		}
		candidate[path] = &fstest.MapFile{Data: data}
		digest := sha256.Sum256(data)
		hashes[path] = hex.EncodeToString(digest[:])
	}
	if err = contract.AdmitAPICandidate(candidate); err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		Status string            `json:"status"`
		Scope  string            `json:"scope"`
		SHA256 map[string]string `json:"sha256"`
	}{"loader-compatible", "run-native-retained-fixtures", hashes})
}
