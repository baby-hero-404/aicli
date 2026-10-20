package sandbox

import (
	"fmt"
	"path/filepath"
)

// SessionDir returns the host directory holding one account's persistent CLI state:
// <root>/<profile>/<accountID>. Pass it (or paths below it) in CommandRequest.SessionMounts so
// each account has its own home state and accounts never share files.
//
// profile and accountID become path elements, so they must be plain names ([A-Za-z0-9._-],
// at most 64 characters, starting alphanumeric); anything that could traverse or escape root
// is rejected. The directory is not created.
func SessionDir(root, profile, accountID string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("session root is required")
	}
	if !nameRe.MatchString(profile) {
		return "", fmt.Errorf("invalid profile %q for session dir", profile)
	}
	if !nameRe.MatchString(accountID) {
		return "", fmt.Errorf("invalid account id %q for session dir", accountID)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(abs, profile, accountID), nil
}
