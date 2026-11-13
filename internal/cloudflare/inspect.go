package cloudflare

import (
	"context"
	"net/url"
)

// --- reading the account: urbex doctor and urbex discover (ADR-0032) -------

// VerifyToken checks the token is valid and active: an account token
// (the one the docs make) against the account, a user token against the
// user. It returns the token's status ("active").
func (c *Client) VerifyToken(ctx context.Context) (string, error) {
	var res struct {
		Status string `json:"status"`
	}
	err := c.do(ctx, "GET", c.account("/tokens/verify"), nil, &res)
	if err != nil && c.accountID != "" {
		if err2 := c.do(ctx, "GET", "/user/tokens/verify", nil, &res); err2 == nil {
			err = nil
		}
	}
	if err != nil {
		return "", err
	}
	return res.Status, nil
}

// Zone is a DNS zone of the account.
type Zone struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// Zones returns the account's zones the token sees.
func (c *Client) Zones(ctx context.Context) ([]Zone, error) {
	var zones []Zone
	q := url.Values{"per_page": {"50"}}
	if c.accountID != "" {
		q.Set("account.id", c.accountID)
	}
	if err := c.do(ctx, "GET", "/zones?"+q.Encode(), nil, &zones); err != nil {
		return nil, err
	}
	return zones, nil
}
