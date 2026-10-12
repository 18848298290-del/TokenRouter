package ent

import "entgo.io/ent/dialect"

// Driver exposes the underlying driver for integrations that execute raw SQL.
func (c *Client) Driver() dialect.Driver {
	return c.driver
}
