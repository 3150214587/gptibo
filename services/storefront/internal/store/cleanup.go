package store

import "context"

// PruneAuth deletes only expired authentication records, in bounded batches.
// Financial and audit history never goes through this retention job.
func (s *Store) PruneAuth(ctx context.Context) error {
	for _, sql := range []string{
		"DELETE FROM auth_challenges WHERE id IN (SELECT id FROM auth_challenges WHERE expires_at<now()-interval '1 hour' ORDER BY expires_at LIMIT 1000)",
		"DELETE FROM auth_sessions WHERE token_hash IN (SELECT token_hash FROM auth_sessions WHERE expires_at<now() ORDER BY expires_at LIMIT 1000)",
		"DELETE FROM auth_limits WHERE reset_at<now() AND key IN (SELECT key FROM auth_limits WHERE reset_at<now() ORDER BY reset_at LIMIT 1000)",
	} {
		if _, e := s.DB.Exec(ctx, sql); e != nil {
			return e
		}
	}
	return nil
}
