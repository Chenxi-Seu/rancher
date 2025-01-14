package keycloakoidc

import (
	"context"
	"net/http"
	"strings"

	"github.com/pkg/errors"
	v32 "github.com/rancher/rancher/pkg/apis/management.cattle.io/v3"
	"github.com/rancher/rancher/pkg/auth/providers/common"
	"github.com/rancher/rancher/pkg/auth/providers/oidc"
	"github.com/rancher/rancher/pkg/auth/tokens"
	client "github.com/rancher/rancher/pkg/client/generated/management/v3"
	v3 "github.com/rancher/rancher/pkg/generated/norman/management.cattle.io/v3"
	"github.com/rancher/rancher/pkg/types/config"
	"github.com/rancher/rancher/pkg/user"
	"github.com/sirupsen/logrus"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	Name      = "keycloakoidc"
	UserType  = "user"
	GroupType = "group"
)

type keyCloakOIDCProvider struct {
	oidc.OpenIDCProvider
}

func Configure(ctx context.Context, mgmtCtx *config.ScaledContext, userMGR user.Manager, tokenMGR *tokens.Manager) common.AuthProvider {
	return &keyCloakOIDCProvider{
		oidc.OpenIDCProvider{
			Name:        Name,
			Type:        client.KeyCloakOIDCConfigType,
			CTX:         ctx,
			AuthConfigs: mgmtCtx.Management.AuthConfigs(""),
			Secrets:     mgmtCtx.Core.Secrets(""),
			UserMGR:     userMGR,
			TokenMGR:    tokenMGR,
		},
	}
}

func (k *keyCloakOIDCProvider) GetName() string {
	return Name
}

func newClient(config *v32.OIDCConfig) (*KeyCloakClient, error) {
	keyCloakClient := &KeyCloakClient{
		httpClient: &http.Client{},
	}
	err := oidc.GetClientWithCertKey(keyCloakClient.httpClient, config.Certificate, config.PrivateKey)
	return keyCloakClient, err
}

func (k *keyCloakOIDCProvider) SearchPrincipals(searchValue, principalType string, token v3.Token) ([]v3.Principal, error) {
	var principals []v3.Principal
	var err error

	config, err := k.GetOIDCConfig()
	if err != nil {
		return principals, err
	}
	keyCloakClient, err := newClient(config)
	if err != nil {
		logrus.Errorf("[keycloak oidc]: error creating new http client: %v", err)
		return principals, err
	}
	accessToken, err := k.TokenMGR.GetSecret(token.UserID, token.AuthProvider, []*v3.Token{&token})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
		accessToken = token.ProviderInfo["access_token"]
	}
	accts, err := keyCloakClient.searchPrincipals(searchValue, principalType, accessToken, config)
	if err != nil {
		logrus.Errorf("[keycloak oidc] problem searching keycloak: %v", err)
	}
	for _, acct := range accts {
		p := k.toPrincipal(acct.Type, acct, &token)
		principals = append(principals, p)
	}
	return principals, nil
}

func (k *keyCloakOIDCProvider) toPrincipal(principalType string, acct account, token *v3.Token) v3.Principal {
	displayName := acct.Name
	if displayName == "" {
		displayName = acct.Username
	}
	princ := v3.Principal{
		ObjectMeta:  metav1.ObjectMeta{Name: k.GetName() + "_" + principalType + "://" + acct.ID},
		DisplayName: displayName,
		LoginName:   acct.Username,
		Provider:    k.GetName(),
		Me:          false,
	}

	if principalType == UserType {
		princ.PrincipalType = UserType
		if token != nil {
			princ.Me = k.IsThisUserMe(token.UserPrincipal, princ)
		}
	} else {
		princ.PrincipalType = GroupType
		if token != nil {
			princ.MemberOf = k.TokenMGR.IsMemberOf(*token, princ)
		}
	}
	return princ
}

func (k *keyCloakOIDCProvider) GetPrincipal(principalID string, token v3.Token) (v3.Principal, error) {
	config, err := k.GetOIDCConfig()
	if err != nil {
		return v3.Principal{}, err
	}
	keyCloakClient, err := newClient(config)
	if err != nil {
		logrus.Errorf("[keycloak oidc]: error creating new http client: %v", err)
		return v3.Principal{}, err
	}
	accessToken, err := k.TokenMGR.GetSecret(token.UserID, token.AuthProvider, []*v3.Token{&token})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return v3.Principal{}, err
		}
		accessToken = token.ProviderInfo["access_token"]
	}
	var externalID string
	parts := strings.SplitN(principalID, ":", 2)
	if len(parts) != 2 {
		return v3.Principal{}, errors.Errorf("invalid id %v", principalID)
	}
	externalID = strings.TrimPrefix(parts[1], "//")
	parts = strings.SplitN(parts[0], "_", 2)
	if len(parts) != 2 {
		return v3.Principal{}, errors.Errorf("invalid id %v", principalID)
	}
	principalType := parts[1]
	searchType := "users"
	if principalType == GroupType {
		searchType = "groups"
	}
	acct, err := keyCloakClient.getFromKeyCloakByID(externalID, searchType, accessToken, config)
	if err != nil {
		return v3.Principal{}, err
	}
	princ := k.toPrincipal(principalType, acct, &token)
	return princ, err
}
