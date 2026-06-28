//go:build !darwin

package applemail

// defaultMailRoot reports that Apple Mail is unavailable off macOS. Discover/New
// return ErrUnsupported, so the builder yields no providers (a single advisory
// warning) and the rest of renomail builds and runs unchanged on other platforms.
func defaultMailRoot() (string, error) {
	return "", ErrUnsupported
}
