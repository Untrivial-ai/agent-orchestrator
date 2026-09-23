package cli

import (
	"encoding/json"
	"fmt"
	"io"
)

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func writeClaimPRCheckout(w io.Writer, branchChanged bool) error {
	// An unchanged branch does not establish that HEAD matches the PR head.
	checkout := "not performed; workspace unchanged"
	if branchChanged {
		checkout = "switched to PR branch"
	}
	_, err := fmt.Fprintf(w, "  checkout: %s\n", checkout)
	return err
}
