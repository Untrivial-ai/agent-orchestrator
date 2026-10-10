package cua

import (
	"context"
	"encoding/xml"
	"errors"
)

// Read the console lock before starting a driver or preparing an owned window.
// Cleanup remains available while the console is locked.
func (a *Adapter) checkScreenUnlocked(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	out, err := a.run(ctx, "/usr/sbin/ioreg", "-a", "-n", "Root", "-d", "1")
	if err != nil {
		return &Error{Code: "screen_lock_unknown", Detail: providerCallDiagnostic(ctx, "ioreg console lock", nil, string(out.Stderr), err, nil), cause: errors.Join(ErrRefused, err, ctx.Err())}
	}
	var state struct {
		Dict struct {
			Values []struct {
				XMLName xml.Name
				Text    string `xml:",chardata"`
			} `xml:",any"`
		} `xml:"dict"`
	}
	if err := xml.Unmarshal(out.Stdout, &state); err != nil {
		return &Error{Code: "screen_lock_unknown", Detail: providerCallDiagnostic(ctx, "ioreg console lock", nil, "invalid console lock plist", nil, err), cause: errors.Join(ErrRefused, err)}
	}
	for i, value := range state.Dict.Values {
		if value.XMLName.Local != "key" || value.Text != "IOConsoleLocked" || i+1 == len(state.Dict.Values) {
			continue
		}
		switch state.Dict.Values[i+1].XMLName.Local {
		case "true":
			return refuse("screen_locked", "macOS console screen is locked: ioreg Root IOConsoleLocked=true")
		case "false":
			return nil
		}
	}
	return refuse("screen_lock_unknown", "ioreg did not prove IOConsoleLocked as a boolean")
}
