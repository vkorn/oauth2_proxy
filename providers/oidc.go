package providers

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/oauth2"

	"github.com/coreos/go-oidc"
	"log"
	"net/http"
	"github.com/bitly/oauth2_proxy/api"
)

type OIDCProvider struct {
	*ProviderData

	Verifier       *oidc.IDTokenVerifier
	GroupValidator func(*SessionState) bool
}

func NewOIDCProvider(p *ProviderData) *OIDCProvider {
	p.ProviderName = "OpenID Connect"
	return &OIDCProvider{ProviderData: p,
		GroupValidator: func(s *SessionState) bool {
			return true
		}}
}

func (p *OIDCProvider) GetEmailAddress(state *SessionState) (email string, err error) {
	req, err := http.NewRequest("GET",
		p.ValidateURL.String(), nil)

	req.Header.Add("Authorization", "Bearer "+state.AccessToken)

	if err != nil {
		log.Printf("failed building request %s", err)
		return "", err
	}

	json, err := api.Request(req)
	if err != nil {
		log.Printf("failed making request %s", err)
		return "", err
	}
	return json.Get("email").String()
}

func (p *OIDCProvider) Redeem(redirectURL, code string) (s *SessionState, err error) {
	ctx := context.Background()
	c := oauth2.Config{
		ClientID:     p.ClientID,
		ClientSecret: p.ClientSecret,
		Endpoint: oauth2.Endpoint{
			TokenURL: p.RedeemURL.String(),
		},
		RedirectURL: redirectURL,
	}
	token, err := c.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("token exchange: %v", err)
	}
	s, err = p.createSessionState(token, ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to update session: %v", err)
	}
	return
}

func (p *OIDCProvider) SetGroupRestriction(groups []string) {
	p.GroupValidator = func(state *SessionState) bool {
		accessToken, err := p.Verifier.Verify(context.Background(), state.AccessToken)
		if err != nil {
			log.Printf("Could not verify access_token: %v for user %s", err, state.User)
			return false
		}

		var roles struct {
			RealmAccess struct {
				Roles []string `json:"roles"`
			} `json:"realm_access"`
		}

		if err := accessToken.Claims(&roles); err != nil {
			log.Printf("Failed to parse access_token claims: %v for user %s", err, state.User)
			return false
		}

		print(len(roles.RealmAccess.Roles))
		for _, existingRole := range roles.RealmAccess.Roles {
			if contains(groups, existingRole) {
				return true
			}
		}

		log.Printf("User %s does not have required roles", state.User)
		return false
	}
}

func contains(slice []string, item string) bool {
	set := make(map[string]struct{}, len(slice))
	for _, s := range slice {
		set[s] = struct{}{}
	}

	_, ok := set[item]
	return ok
}

func (p *OIDCProvider) ValidateGroup(session *SessionState) bool {
	return p.GroupValidator(session)
}

func (p *OIDCProvider) RefreshSessionIfNeeded(s *SessionState) (bool, error) {
	if s == nil || s.ExpiresOn.After(time.Now()) || s.RefreshToken == "" {
		return false, nil
	}

	origExpiration := s.ExpiresOn

	err := p.redeemRefreshToken(s)
	if err != nil {
		return false, fmt.Errorf("unable to redeem refresh token: %v", err)
	}

	fmt.Printf("refreshed id token %s (expired on %s)\n", s, origExpiration)
	return true, nil
}

func (p *OIDCProvider) redeemRefreshToken(s *SessionState) (err error) {
	c := oauth2.Config{
		ClientID:     p.ClientID,
		ClientSecret: p.ClientSecret,
		Endpoint: oauth2.Endpoint{
			TokenURL: p.RedeemURL.String(),
		},
	}
	ctx := context.Background()
	t := &oauth2.Token{
		RefreshToken: s.RefreshToken,
		Expiry:       time.Now().Add(-time.Hour),
	}
	token, err := c.TokenSource(ctx, t).Token()
	if err != nil {
		return fmt.Errorf("failed to get token: %v", err)
	}
	newSession, err := p.createSessionState(token, ctx)
	if err != nil {
		return fmt.Errorf("unable to update session: %v", err)
	}
	s.AccessToken = newSession.AccessToken
	s.IdToken = newSession.IdToken
	s.RefreshToken = newSession.RefreshToken
	s.ExpiresOn = newSession.ExpiresOn
	s.Email = newSession.Email
	return
}

func (p *OIDCProvider) createSessionState(token *oauth2.Token, ctx context.Context) (*SessionState, error) {
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return nil, fmt.Errorf("token response did not contain an id_token")
	}

	// Parse and verify ID Token payload.
	idToken, err := p.Verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("could not verify id_token: %v", err)
	}

	// Extract custom claims.
	var claims struct {
		Email    string `json:"email"`
		Verified *bool  `json:"email_verified"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("failed to parse id_token claims: %v", err)
	}

	if claims.Email == "" {
		return nil, fmt.Errorf("id_token did not contain an email")
	}
	if claims.Verified != nil && !*claims.Verified {
		return nil, fmt.Errorf("email in id_token (%s) isn't verified", claims.Email)
	}

	return &SessionState{
		AccessToken:  token.AccessToken,
		IdToken:      rawIDToken,
		RefreshToken: token.RefreshToken,
		ExpiresOn:    token.Expiry,
		Email:        claims.Email,
	}, nil
}

func (p *OIDCProvider) ValidateSessionState(s *SessionState) bool {
	ctx := context.Background()
	_, err := p.Verifier.Verify(ctx, s.IdToken)
	if err != nil {
		return false
	}

	return true
}
