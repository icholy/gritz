package apiserver

import (
	"context"
	"testing"

	"github.com/icholy/gritz/internal/auth/apiauth"
	"github.com/icholy/gritz/internal/auth/authscope"
	"github.com/icholy/gritz/internal/store/teststore"
)

func createCtx(t *testing.T, org *teststore.Org) context.Context {
	t.Helper()
	// Test callers hold the admin wildcard, matching the cookie/app sessions
	// these handler tests stand in for; scope narrowing is exercised separately.
	return apiauth.WithUser(t.Context(), &apiauth.UserInfo{
		ID:     org.UserID,
		OrgID:  org.OrgID,
		Scopes: authscope.Admin(),
	})
}
