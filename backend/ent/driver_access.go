package ent

import "entgo.io/ent/dialect"

// Raw SQL integrations need the same database connection infrastructure as Ent
// queries. The application extracts its SQL pool from this driver, so Ent and
// repositories that execute SQL participate in the application's shared database
// lifecycle.

// Driver returns the database driver held by this client. The returned driver
// remains shared with Ent; integrations that extract its SQL pool use the
// client's existing lifecycle and leave closure to the client owner.
func (c *Client) Driver() dialect.Driver {
	return c.driver
}
