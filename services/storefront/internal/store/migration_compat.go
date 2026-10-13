package store

// The public distribution initializes a fresh database. No private deployment
// migration hashes or compatibility exceptions are carried into this history.
func migrationMatches(_ string, data []byte, stored string) bool {
	return checksum(data) == stored
}
