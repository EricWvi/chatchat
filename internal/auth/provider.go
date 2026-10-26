package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"chatchat/internal/store"
)

// provider is the discovered OIDC configuration.
type provider struct {
	oidc     *oidc.Provider
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// lazyProvider discovers the issuer on first use and caches the result, so
// the service starts (and serves cached pages) while Authelia is down.
type lazyProvider struct {
	issuer, clientID, clientSecret, redirectURL string
	http                                        *http.Client

	mu  sync.Mutex
	got *provider
}

func (l *lazyProvider) get(ctx context.Context) (*provider, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.got != nil {
		return l.got, nil
	}
	ctx, cancel := context.WithTimeout(l.context(ctx), 15*time.Second)
	defer cancel()
	p, err := oidc.NewProvider(ctx, l.issuer)
	if err != nil {
		return nil, fmt.Errorf("discover %s: %w", l.issuer, err)
	}
	l.got = &provider{
		oidc: p,
		oauth: oauth2.Config{
			ClientID:     l.clientID,
			ClientSecret: l.clientSecret,
			Endpoint:     p.Endpoint(),
			RedirectURL:  l.redirectURL,
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		},
		verifier: p.Verifier(&oidc.Config{ClientID: l.clientID}),
	}
	return l.got, nil
}

// context routes the OIDC and OAuth2 libraries through the configured
// HTTP client.
func (l *lazyProvider) context(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, l.http)
	return oidc.ClientContext(ctx, l.http)
}

// identify exchanges an authorization code and returns who signed in.
// The ID token's signature, issuer, audience, expiry and nonce are checked;
// display attributes come from UserInfo because Authelia keeps ID tokens
// minimal by default.
func (l *lazyProvider) identify(ctx context.Context, code string, flow loginFlow) (store.Identity, error) {
	p, err := l.get(ctx)
	if err != nil {
		return store.Identity{}, err
	}
	ctx, cancel := context.WithTimeout(l.context(ctx), 15*time.Second)
	defer cancel()

	token, err := p.oauth.Exchange(ctx, code, oauth2.VerifierOption(flow.Verifier))
	if err != nil {
		return store.Identity{}, fmt.Errorf("exchange code: %w", err)
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		return store.Identity{}, errors.New("token response has no id_token")
	}
	idToken, err := p.verifier.Verify(ctx, raw)
	if err != nil {
		return store.Identity{}, fmt.Errorf("verify id_token: %w", err)
	}
	if idToken.Nonce != flow.Nonce {
		return store.Identity{}, errors.New("id_token nonce mismatch")
	}

	var profile struct {
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
		Email             string `json:"email"`
	}
	idToken.Claims(&profile)
	if info, err := p.oidc.UserInfo(ctx, oauth2.StaticTokenSource(token)); err == nil && info.Subject == idToken.Subject {
		info.Claims(&profile)
	}
	name := profile.Name
	if name == "" {
		name = profile.PreferredUsername
	}
	return store.Identity{
		Issuer:  idToken.Issuer,
		Subject: idToken.Subject,
		Name:    name,
		Email:   profile.Email,
	}, nil
}
