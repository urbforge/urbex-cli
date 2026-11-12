package cloudflare

import (
	"context"
	"net/url"
)

// --- reading the account: urbex doctor and urbex discover (ADR-0032) -------

// VerifyToken checks the token is valid and active: an account token
// (the one the docs make) against the client's account, a user token
// against the user. It returns the token's status ("active").
func (c *Client) VerifyToken(ctx context.Context) (string, error) {
	return c.VerifyAccountToken(ctx, c.accountID)
}

// VerifyAccountToken is VerifyToken against account; with none, or when
// the account refuses it, against the user.
func (c *Client) VerifyAccountToken(ctx context.Context, account string) (string, error) {
	var res struct {
		Status string `json:"status"`
	}
	var err error
	if account != "" {
		if err = c.do(ctx, "GET", "/accounts/"+account+"/tokens/verify", nil, &res); err == nil {
			return res.Status, nil
		}
	}
	if err2 := c.do(ctx, "GET", "/user/tokens/verify", nil, &res); err2 != nil {
		if err == nil {
			err = err2
		}
		return "", err
	}
	return res.Status, nil
}

// Zone is a DNS zone of the account.
type Zone struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Account struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"account"`
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
